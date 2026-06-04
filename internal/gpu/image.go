package gpu

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// GenerateImage handles image generation by routing to the local AUTOMATIC1111 or ComfyUI instance.
func GenerateImage(ctx context.Context, req ImageRequest, generator string, dataDir string) (ImageResult, error) {
	startTime := time.Now()
	var imgBytes []byte
	var err error

	o := GetOrchestrator()
	if o != nil {
		taskID := fmt.Sprintf("image-gen-%d", time.Now().UnixNano())
		res, err := o.ReserveImageGen(taskID, req.Model, req.Width, req.Height, 50)
		if err != nil {
			return ImageResult{}, err
		}
		defer o.ReleaseReservation(res)
		if err := o.ActivateReservation(res); err != nil {
			return ImageResult{}, err
		}
	}

	svc, errSvc := DiscoverImageGenServices(GetConfiguredURL())
	if errSvc != nil {
		return ImageResult{}, errSvc
	}

	if generator == "automatic1111" {
		imgBytes, err = generateAutomatic1111(ctx, req, svc.URL)
	} else if generator == "comfyui" {
		imgBytes, err = generateComfyUI(ctx, req, svc.URL)
	} else {
		return ImageResult{}, fmt.Errorf("no active image generation engine detected on this node")
	}

	if err != nil {
		return ImageResult{}, err
	}

	// Ensure output directory exists
	outDir := filepath.Join(dataDir, "storage", "generated")
	if err := os.MkdirAll(outDir, 0750); err != nil {
		return ImageResult{}, fmt.Errorf("failed to create output directory: %w", err)
	}

	// Save file with timestamp
	filename := fmt.Sprintf("%d.png", time.Now().UnixNano())
	destPath := filepath.Join(outDir, filename)
	if err := os.WriteFile(destPath, imgBytes, 0640); err != nil {
		return ImageResult{}, fmt.Errorf("failed to save generated image: %w", err)
	}

	return ImageResult{
		StoragePath: "generated/" + filename,
		NodeID:      "local",
		ElapsedMS:   time.Since(startTime).Milliseconds(),
	}, nil
}

func generateAutomatic1111(ctx context.Context, req ImageRequest, baseURL string) ([]byte, error) {
	width := req.Width
	if width <= 0 {
		width = 1024
	}
	height := req.Height
	if height <= 0 {
		height = 1024
	}
	steps := req.Steps
	if steps <= 0 {
		steps = 20
	}

	payload := map[string]any{
		"prompt":           req.Prompt,
		"negative_prompt":  req.NegativePrompt,
		"steps":            steps,
		"width":            width,
		"height":           height,
	}

	if req.Model != "" {
		// Try to set model checkpoint if supported
		payload["override_settings"] = map[string]string{
			"sd_model_checkpoint": req.Model,
		}
	}

	bodyBytes, _ := json.Marshal(payload)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/sdapi/v1/txt2img", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Minute} // Generates can take time
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to AUTOMATIC1111: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("AUTOMATIC1111 returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Images []string `json:"images"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode AUTOMATIC1111 response: %w", err)
	}

	if len(result.Images) == 0 {
		return nil, fmt.Errorf("no images returned by AUTOMATIC1111")
	}

	imgData, err := base64.StdEncoding.DecodeString(result.Images[0])
	if err != nil {
		return nil, fmt.Errorf("failed to decode base64 image: %w", err)
	}

	return imgData, nil
}

func generateComfyUI(ctx context.Context, req ImageRequest, baseURL string) ([]byte, error) {
	width := req.Width
	if width <= 0 {
		width = 1024
	}
	height := req.Height
	if height <= 0 {
		height = 1024
	}
	steps := req.Steps
	if steps <= 0 {
		steps = 20
	}
	ckpt := req.Model
	if ckpt == "" {
		ckpt = "v1-5-pruned-emaonly.ckpt"
	}

	// Build default workflow graph JSON
	workflow := map[string]any{
		"3": map[string]any{
			"class_type": "KSampler",
			"inputs": map[string]any{
				"cfg":          8.0,
				"denoise":      1.0,
				"latent_image": []any{"5", 0},
				"model":        []any{"4", 0},
				"noise_seed":   time.Now().UnixNano() / 1000000,
				"positive":     []any{"6", 0},
				"negative":     []any{"7", 0},
				"sampler_name": "euler",
				"scheduler":    "normal",
				"steps":        steps,
			},
		},
		"4": map[string]any{
			"class_type": "CheckpointLoaderSimple",
			"inputs": map[string]any{
				"ckpt_name": ckpt,
			},
		},
		"5": map[string]any{
			"class_type": "EmptyLatentImage",
			"inputs": map[string]any{
				"batch_size": 1,
				"height":     height,
				"width":      width,
			},
		},
		"6": map[string]any{
			"class_type": "CLIPTextEncode",
			"inputs": map[string]any{
				"clip": []any{"4", 1},
				"text": req.Prompt,
			},
		},
		"7": map[string]any{
			"class_type": "CLIPTextEncode",
			"inputs": map[string]any{
				"clip": []any{"4", 1},
				"text": req.NegativePrompt,
			},
		},
		"8": map[string]any{
			"class_type": "VAEDecode",
			"inputs": map[string]any{
				"samples": []any{"3", 0},
				"vae":     []any{"4", 2},
			},
		},
		"9": map[string]any{
			"class_type": "SaveImage",
			"inputs": map[string]any{
				"filename_prefix": "OpenFabric",
				"images":          []any{"8", 0},
			},
		},
	}

	// 1. Submit prompt
	submitPayload := map[string]any{
		"prompt": workflow,
	}
	submitBytes, _ := json.Marshal(submitPayload)
	submitReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/prompt", bytes.NewReader(submitBytes))
	if err != nil {
		return nil, err
	}
	submitReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	submitResp, err := client.Do(submitReq)
	if err != nil {
		return nil, fmt.Errorf("failed to submit prompt to ComfyUI: %w", err)
	}
	defer submitResp.Body.Close()

	if submitResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(submitResp.Body)
		return nil, fmt.Errorf("ComfyUI submit returned status %d: %s", submitResp.StatusCode, string(respBody))
	}

	var submitResult struct {
		PromptID string `json:"prompt_id"`
	}
	if err := json.NewDecoder(submitResp.Body).Decode(&submitResult); err != nil {
		return nil, fmt.Errorf("failed to parse ComfyUI submit response: %w", err)
	}

	// 2. Poll for completion
	var filename string
	pollTicker := time.NewTicker(1 * time.Second)
	defer pollTicker.Stop()

	pollTimeout := time.After(10 * time.Minute)
	completed := false

	for !completed {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-pollTimeout:
			return nil, fmt.Errorf("image generation timed out on ComfyUI")
		case <-pollTicker.C:
			historyReq, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/history/"+submitResult.PromptID, nil)
			if err != nil {
				continue
			}
			historyResp, err := client.Do(historyReq)
			if err != nil {
				continue
			}
			
			if historyResp.StatusCode == http.StatusOK {
				var historyResult map[string]any
				if err := json.NewDecoder(historyResp.Body).Decode(&historyResult); err == nil {
					if jobData, exists := historyResult[submitResult.PromptID]; exists {
						if jobMap, ok := jobData.(map[string]any); ok {
							if outputs, exists := jobMap["outputs"]; exists {
								if outputsMap, ok := outputs.(map[string]any); ok {
									// ComfyUI outputs image details under the SaveImage node index (node 9)
									if nodeOutput, exists := outputsMap["9"]; exists {
										if nodeOutputMap, ok := nodeOutput.(map[string]any); ok {
											if images, exists := nodeOutputMap["images"]; exists {
												if imgList, ok := images.([]any); ok && len(imgList) > 0 {
													if firstImg, ok := imgList[0].(map[string]any); ok {
														if fname, ok := firstImg["filename"].(string); ok {
															filename = fname
															completed = true
														}
													}
												}
											}
										}
									}
								}
							}
						}
					}
				}
			}
			historyResp.Body.Close()
		}
	}

	if filename == "" {
		return nil, fmt.Errorf("comfyUI task completed but failed to find output filename")
	}

	// 3. Fetch image bytes
	viewURL := fmt.Sprintf("%s/view?filename=%s&type=output", baseURL, filename)
	viewReq, err := http.NewRequestWithContext(ctx, http.MethodGet, viewURL, nil)
	if err != nil {
		return nil, err
	}
	viewResp, err := client.Do(viewReq)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch generated image from ComfyUI: %w", err)
	}
	defer viewResp.Body.Close()

	if viewResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch image from ComfyUI: status %d", viewResp.StatusCode)
	}

	imgData, err := io.ReadAll(viewResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read ComfyUI image response: %w", err)
	}

	return imgData, nil
}
