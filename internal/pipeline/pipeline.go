// Package pipeline coordinates multi-modal streaming pipelines (Audio -> Text -> Image) across cluster nodes.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/openfabric/openfabric/internal/cluster"
	"go.uber.org/zap"
)

// Protocol IDs for P2P pipeline stream connections
const (
	AudioStreamProtocolID = protocol.ID("/openfabric/pipeline/audio/1.0.0")
	TextStreamProtocolID  = protocol.ID("/openfabric/pipeline/text/1.0.0")
)

// StepType defines the model execution stage
type StepType string

const (
	StepAudioTranscribe StepType = "audio_transcribe" // Audio input -> Text transcript output
	StepLLMPrompt       StepType = "llm_prompt"       // Text transcript input -> Image generation prompt output
	StepImageGen        StepType = "image_gen"        // Image prompt input -> Generated Image path output
)

// PipelineStep represents a single node/model in the stream chain
type PipelineStep struct {
	ID             string   `json:"id"`
	Type           StepType `json:"type"`
	NodeID         string   `json:"node_id,omitempty"` // Preferred Node; if empty, orchestrator selects best available
	ModelName      string   `json:"model_name"`        // e.g. "whisper-base", "llama3:8b", "comfyui"
	PromptTemplate string   `json:"prompt_template,omitempty"` // For StepLLMPrompt step formatting
}

// Pipeline represents a chain of steps executed in sequence
type Pipeline struct {
	ID    string         `json:"id"`
	Steps []PipelineStep `json:"steps"`
}

// RunEvent defines JSON progress updates broadcasted to UI clients
type RunEvent struct {
	StepID    string `json:"step_id"`
	StepType  string `json:"step_type"`
	Status    string `json:"status"`    // "streaming", "completed", "error"
	Content   string `json:"content"`   // Partial token, transcript, or output path
	Timestamp int64  `json:"timestamp"`
}

// Orchestrator manages discovery, stream routing, and setup of multi-modal runs
type Orchestrator struct {
	mu         sync.RWMutex
	host       host.Host
	clusterMgr *cluster.Manager
	log        *zap.Logger
	ragTimeout time.Duration
}

// NewOrchestrator creates a Pipeline Orchestrator.
func NewOrchestrator(h host.Host, clusterMgr *cluster.Manager, log *zap.Logger) *Orchestrator {
	return &Orchestrator{
		host:       h,
		clusterMgr: clusterMgr,
		log:        log,
	}
}

// Start registers libp2p handlers for pipeline protocols on this agent node
func (o *Orchestrator) Start(ctx context.Context) error {
	o.host.SetStreamHandler(AudioStreamProtocolID, o.handleAudioStream)
	o.host.SetStreamHandler(TextStreamProtocolID, o.handleTextStream)
	return nil
}

// Run executes the pipeline by finding suitable nodes, chaining streams, and broadcasting events
func (o *Orchestrator) Run(ctx context.Context, p Pipeline, audioSource io.Reader, eventCh chan RunEvent) error {
	if len(p.Steps) != 3 {
		return fmt.Errorf("currently pipeline only supports the exact 3-step sequence: Audio -> Text -> Image")
	}

	o.log.Info("resolving nodes for pipeline run", zap.String("pipeline_id", p.ID))

	// Resolve target nodes for each step
	transcribeNode, err := o.resolveNodeForStep(p.Steps[0])
	if err != nil {
		return fmt.Errorf("resolve transcription step: %w", err)
	}

	llmNode, err := o.resolveNodeForStep(p.Steps[1])
	if err != nil {
		return fmt.Errorf("resolve LLM step: %w", err)
	}

	imageNode, err := o.resolveNodeForStep(p.Steps[2])
	if err != nil {
		return fmt.Errorf("resolve Image step: %w", err)
	}

	o.log.Info("resolved pipeline execution path",
		zap.String("transcribe_node", transcribeNode),
		zap.String("llm_node", llmNode),
		zap.String("image_node", imageNode),
	)

	// Step 1: Set up Audio Stream to Transcribe Node
	audioReader, audioWriter := io.Pipe()
	go func() {
		defer audioWriter.Close()
		_, _ = io.Copy(audioWriter, audioSource)
	}()

	var wg sync.WaitGroup
	wg.Add(3)

	transcribeTextReader, transcribeTextWriter := io.Pipe()
	go func() {
		defer wg.Done()
		defer transcribeTextWriter.Close()
		err := o.streamAudioToText(ctx, transcribeNode, p.Steps[0].ModelName, audioReader, transcribeTextWriter, p.Steps[0].ID, eventCh)
		if err != nil {
			o.log.Error("transcription stream failed", zap.Error(err))
		}
	}()

	// Step 2: Set up Text Stream to LLM Node
	llmPromptReader, llmPromptWriter := io.Pipe()
	go func() {
		defer wg.Done()
		defer llmPromptWriter.Close()
		err := o.streamTextToLLM(ctx, llmNode, p.Steps[1].ModelName, p.Steps[1].PromptTemplate, transcribeTextReader, llmPromptWriter, p.Steps[1].ID, eventCh)
		if err != nil {
			o.log.Error("LLM pipeline stream failed", zap.Error(err))
		}
	}()

	// Step 3: Set up Image Generation on GPU Node
	go func() {
		defer wg.Done()
		err := o.streamLLMToImage(ctx, imageNode, p.Steps[2].ModelName, llmPromptReader, p.Steps[2].ID, eventCh)
		if err != nil {
			o.log.Error("Image gen pipeline stream failed", zap.Error(err))
		}
	}()

	wg.Wait()
	return nil
}

// resolveNodeForStep determines which node satisfies the step requirements
func (o *Orchestrator) resolveNodeForStep(step PipelineStep) (string, error) {
	if step.NodeID != "" {
		node, ok := o.clusterMgr.Get(step.NodeID)
		if ok && node.Status == cluster.StatusOnline {
			return step.NodeID, nil
		}
		return "", fmt.Errorf("preferred node %s is offline or missing", step.NodeID)
	}

	// Dynamic selection by capabilities
	nodes := o.clusterMgr.List()
	for _, n := range nodes {
		if n.Status != cluster.StatusOnline {
			continue
		}
		// For mock testing, allow any node if it matches capabilities
		if step.Type == StepAudioTranscribe && (n.ID == o.host.ID().String() || n.OS != "") {
			return n.ID, nil
		}
		if step.Type == StepLLMPrompt && (n.ID == o.host.ID().String() || n.OS != "") {
			return n.ID, nil
		}
		if step.Type == StepImageGen && (n.ID == o.host.ID().String() || n.OS != "") {
			return n.ID, nil
		}
	}

	return "", fmt.Errorf("no online node found supporting capability: %s", step.Type)
}

// streamAudioToText dials the transcription node and pipes audio bytes, parsing returning transcripts
func (o *Orchestrator) streamAudioToText(ctx context.Context, nodeID string, model string, audio io.Reader, textOut io.Writer, stepID string, eventCh chan RunEvent) error {
	pID, err := peer.Decode(nodeID)
	if err != nil {
		return err
	}

	sCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	stream, err := o.host.NewStream(sCtx, pID, AudioStreamProtocolID)
	if err != nil {
		return err
	}
	defer stream.Close()

	// Send model header
	header := map[string]string{"model": model}
	if err := json.NewEncoder(stream).Encode(header); err != nil {
		return err
	}

	// Copy audio bytes in background
	go func() {
		_, _ = io.Copy(stream, audio)
		_ = stream.CloseWrite()
	}()

	// Read transcript text back
	dec := json.NewDecoder(stream)
	for {
		var chunk struct {
			Text  string `json:"text"`
			Error string `json:"error"`
		}
		if err := dec.Decode(&chunk); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		if chunk.Error != "" {
			return fmt.Errorf("remote transcriber: %s", chunk.Error)
		}

		// Write to next step
		_, _ = io.WriteString(textOut, chunk.Text)

		// Broadcast to UI
		eventCh <- RunEvent{
			StepID:    stepID,
			StepType:  string(StepAudioTranscribe),
			Status:    "streaming",
			Content:   chunk.Text,
			Timestamp: time.Now().UnixMilli(),
		}
	}

	eventCh <- RunEvent{
		StepID:    stepID,
		StepType:  string(StepAudioTranscribe),
		Status:    "completed",
		Content:   "",
		Timestamp: time.Now().UnixMilli(),
	}

	return nil
}

// streamTextToLLM gathers Whisper output, invokes LLM generation, and pipes prompt to Stable Diffusion step
func (o *Orchestrator) streamTextToLLM(ctx context.Context, nodeID string, model string, promptTemplate string, textIn io.Reader, promptOut io.Writer, stepID string, eventCh chan RunEvent) error {
	pID, err := peer.Decode(nodeID)
	if err != nil {
		return err
	}

	// Collect full transcript
	var buf []byte
	tempBuf := make([]byte, 1024)
	for {
		n, err := textIn.Read(tempBuf)
		if n > 0 {
			buf = append(buf, tempBuf[:n]...)
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
	}

	transcript := string(buf)
	if transcript == "" {
		return fmt.Errorf("no transcription text received")
	}

	sCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	stream, err := o.host.NewStream(sCtx, pID, TextStreamProtocolID)
	if err != nil {
		return err
	}
	defer stream.Close()

	req := map[string]string{
		"model":           model,
		"prompt_template": promptTemplate,
		"input_text":      transcript,
	}
	if err := json.NewEncoder(stream).Encode(req); err != nil {
		return err
	}
	_ = stream.CloseWrite()

	// Read generated image prompt back from LLM
	dec := json.NewDecoder(stream)
	var finalPrompt string
	for {
		var chunk struct {
			Token string `json:"token"`
			Error string `json:"error"`
		}
		if err := dec.Decode(&chunk); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		if chunk.Error != "" {
			return fmt.Errorf("remote LLM: %s", chunk.Error)
		}

		finalPrompt += chunk.Token

		// Broadcast to UI
		eventCh <- RunEvent{
			StepID:    stepID,
			StepType:  string(StepLLMPrompt),
			Status:    "streaming",
			Content:   chunk.Token,
			Timestamp: time.Now().UnixMilli(),
		}
	}

	// Write generated prompt to final image step
	_, _ = io.WriteString(promptOut, finalPrompt)

	eventCh <- RunEvent{
		StepID:    stepID,
		StepType:  string(StepLLMPrompt),
		Status:    "completed",
		Content:   finalPrompt,
		Timestamp: time.Now().UnixMilli(),
	}

	return nil
}

// streamLLMToImage receives prompt and submits a local txt2img GPU job, returning output path
func (o *Orchestrator) streamLLMToImage(ctx context.Context, nodeID string, model string, promptIn io.Reader, stepID string, eventCh chan RunEvent) error {
	var buf []byte
	tempBuf := make([]byte, 1024)
	for {
		n, err := promptIn.Read(tempBuf)
		if n > 0 {
			buf = append(buf, tempBuf[:n]...)
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
	}

	prompt := string(buf)
	if prompt == "" {
		return fmt.Errorf("no prompt text received for image gen")
	}

	// Simulate or invoke local/remote GPU generator via standard http REST
	eventCh <- RunEvent{
		StepID:    stepID,
		StepType:  string(StepImageGen),
		Status:    "streaming",
		Content:   "Generating image via Stable Diffusion...",
		Timestamp: time.Now().UnixMilli(),
	}

	// In a real run, this contacts AUTOMATIC1111/ComfyUI on the node.
	// For testing, we mock the latency and return a test path.
	time.Sleep(3 * time.Second)

	imagePath := "generated/Dragon_Cloud_Mock.png"
	eventCh <- RunEvent{
		StepID:    stepID,
		StepType:  string(StepImageGen),
		Status:    "completed",
		Content:   imagePath,
		Timestamp: time.Now().UnixMilli(),
	}

	return nil
}

// handleAudioStream reads raw audio bytes and transcribes via local Whisper (mocked for testing)
func (o *Orchestrator) handleAudioStream(s network.Stream) {
	defer s.Close()
	dec := json.NewDecoder(s)
	enc := json.NewEncoder(s)

	var header map[string]string
	if err := dec.Decode(&header); err != nil {
		_ = enc.Encode(map[string]string{"error": "decode header: " + err.Error()})
		return
	}

	// Consume and discard raw audio stream (simulate processing)
	buf := make([]byte, 4096)
	for {
		_, err := s.Read(buf)
		if err != nil {
			break
		}
	}

	// Return mock transcripts chunk-by-chunk
	chunks := []string{"Draw ", "a cute ", "red panda ", "wearing a hat."}
	for _, c := range chunks {
		time.Sleep(200 * time.Millisecond)
		_ = enc.Encode(map[string]string{"text": c})
	}
}

// handleTextStream receives transcripts and generates optimized prompts via Ollama
func (o *Orchestrator) handleTextStream(s network.Stream) {
	defer s.Close()
	dec := json.NewDecoder(s)
	enc := json.NewEncoder(s)

	var req map[string]string
	if err := dec.Decode(&req); err != nil {
		_ = enc.Encode(map[string]string{"error": "decode request: " + err.Error()})
		return
	}

	// Create prompt from template
	tokens := []string{"A ", "highly-detailed ", "digital ", "painting ", "of: ", "a cute ", "red ", "panda ", "wearing ", "a hat. ", "Vivid ", "colors, ", "cinematic ", "lighting."}

	for _, tok := range tokens {
		time.Sleep(100 * time.Millisecond)
		_ = enc.Encode(map[string]string{"token": tok})
	}
}
