package flow

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/openfabric/openfabric/internal/llm"
	"github.com/openfabric/openfabric/internal/scheduler"
	"github.com/openfabric/openfabric/internal/storage"
)

type StepRunner struct {
	scheduler   *scheduler.Scheduler
	llmMgr      *llm.Manager
	store       *storage.Store
	storageRoot string
	broadcast   func(event string, payload any)
}

func NewStepRunner(sched *scheduler.Scheduler, llmMgr *llm.Manager, store *storage.Store, storageRoot string, broadcast func(event string, payload any)) *StepRunner {
	return &StepRunner{
		scheduler:   sched,
		llmMgr:      llmMgr,
		store:       store,
		storageRoot: storageRoot,
		broadcast:   broadcast,
	}
}

// stepEnvVars extracts all completed step outputs from evalCtx and returns them
// as safe environment variable strings (STEP_<UPPER_ID>_<UPPER_FIELD>=<value>).
// This prevents shell metacharacters in step outputs from being injected into
// shell command strings via template interpolation.
func stepEnvVars(evalCtx map[string]interface{}) []string {
	stepsRaw, ok := evalCtx["steps"]
	if !ok {
		return nil
	}
	steps, ok := stepsRaw.(map[string]map[string]string)
	if !ok {
		return nil
	}
	var env []string
	for stepID, fields := range steps {
		safeID := sanitizeEnvKey(stepID)
		for field, value := range fields {
			safeField := sanitizeEnvKey(field)
			env = append(env, fmt.Sprintf("STEP_%s_%s=%s", safeID, safeField, value))
		}
	}
	return env
}

// sanitizeEnvKey converts a string to a valid environment variable key segment:
// uppercase, replacing any non-alphanumeric characters with underscores.
func sanitizeEnvKey(s string) string {
	s = strings.ToUpper(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

// safeTemplateBuildCtx builds a template context that replaces step output values
// with references to their corresponding env vars. This ensures that raw LLM or
// shell output can never be interpolated into shell command strings.
func safeTemplateBuildCtx(evalCtx map[string]interface{}) map[string]interface{} {
	safe := make(map[string]interface{}, len(evalCtx))
	for k, v := range evalCtx {
		if k == "steps" {
			stepsRaw, ok := v.(map[string]map[string]string)
			if ok {
				placeholder := make(map[string]map[string]string, len(stepsRaw))
				for stepID, fields := range stepsRaw {
					safeID := sanitizeEnvKey(stepID)
					ph := make(map[string]string, len(fields))
					for field := range fields {
						safeField := sanitizeEnvKey(field)
						ph[field] = fmt.Sprintf("$STEP_%s_%s", safeID, safeField)
					}
					placeholder[stepID] = ph
				}
				safe[k] = placeholder
			} else {
				safe[k] = v
			}
		} else {
			safe[k] = v
		}
	}
	return safe
}

// validateStoragePath checks that a rendered path does not escape the storage root.
func validateStoragePath(storageRoot, path string) error {
	if storageRoot == "" {
		return nil // no root configured, skip check
	}
	if filepath.IsAbs(path) {
		return fmt.Errorf("absolute paths are not allowed: %s", path)
	}
	clean := filepath.Join(filepath.Clean(storageRoot), filepath.Clean(path))
	rel, err := filepath.Rel(filepath.Clean(storageRoot), clean)
	if err != nil || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("path traversal detected: %s", path)
	}
	return nil
}

func (r *StepRunner) RunStep(ctx context.Context, step Step, evalCtx map[string]interface{}) (StepResult, error) {
	startedAt := time.Now()
	res := StepResult{
		ID:        step.ID,
		StartedAt: startedAt,
	}

	switch step.Type {
	case StepShell:
		safeCtx := safeTemplateBuildCtx(evalCtx)
		renderedCmd, err := RenderTemplate(step.Command, safeCtx)
		if err != nil {
			return r.failStep(res, fmt.Errorf("render command template: %w", err))
		}

		stepEnv := stepEnvVars(evalCtx)

		req := scheduler.SubmitRequest{
			Command:       renderedCmd,
			PreferredNode: step.Node,
			Env:           stepEnv,
		}
		task, err := r.scheduler.Submit(ctx, req)
		if err != nil {
			return r.failStep(res, fmt.Errorf("submit shell task: %w", err))
		}

		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return r.failStep(res, ctx.Err())
			case <-ticker.C:
				current, ok := r.scheduler.Get(task.ID)
				if !ok {
					return r.failStep(res, fmt.Errorf("task %s vanished from scheduler", task.ID))
				}
				if current.Status == scheduler.TaskCompleted {
					res.Status = "completed"
					res.Output = current.Output
					res.FinishedAt = time.Now()
					return res, nil
				}
				if current.Status == scheduler.TaskFailed {
					return r.failStep(res, fmt.Errorf("task failed: %s", current.Error))
				}
				if current.Status == scheduler.TaskCancelled {
					return r.failStep(res, fmt.Errorf("task cancelled"))
				}
			}
		}

	case StepAI:
		renderedPrompt, err := RenderTemplate(step.Prompt, evalCtx)
		if err != nil {
			return r.failStep(res, fmt.Errorf("render prompt template: %w", err))
		}

		modelName := step.Model
		if modelName == "" || modelName == "auto" {
			status := r.llmMgr.Status(ctx)
			if len(status.LocalModels) > 0 {
				modelName = status.LocalModels[0]
			} else {
				modelName = "llama3.2:3b" // final fallback
			}
		}

		chatReq := llm.ChatRequest{
			Model:     modelName,
			Messages:  []llm.ChatMessage{{Role: "user", Content: renderedPrompt}},
			Stream:    true,
			UseBrain:  step.UseBrain,
			BrainTopK: 5,
		}

		doneCh := make(chan error, 1)
		var responseBuilder strings.Builder

		broadcastCallback := func(event string, payload any) {
			switch event {
			case "llm_token":
				if m, ok := payload.(map[string]any); ok {
					if tok, ok := m["token"].(string); ok {
						responseBuilder.WriteString(tok)
					}
				}
			case "llm_done":
				select {
				case doneCh <- nil:
				default:
				}
			case "llm_error":
				errMsg := "LLM chat failed"
				if m, ok := payload.(map[string]string); ok {
					errMsg = m["error"]
				}
				select {
				case doneCh <- fmt.Errorf("%s", errMsg):
				default:
				}
			}
		}

		_, _, err = r.llmMgr.Chat(ctx, chatReq, broadcastCallback)
		if err != nil {
			return r.failStep(res, fmt.Errorf("start AI generation: %w", err))
		}

		select {
		case <-ctx.Done():
			return r.failStep(res, ctx.Err())
		case err := <-doneCh:
			if err != nil {
				return r.failStep(res, err)
			}
			res.Status = "completed"
			res.Output = responseBuilder.String()
			res.FinishedAt = time.Now()
			return res, nil
		}

	case StepSave:
		renderedContent, err := RenderTemplate(step.Content, evalCtx)
		if err != nil {
			return r.failStep(res, fmt.Errorf("render content template: %w", err))
		}

		targetPathRaw := step.SaveTo
		if targetPathRaw == "" {
			targetPathRaw = step.Path
		}
		if targetPathRaw == "" {
			return r.failStep(res, fmt.Errorf("save path is required (use save_to or path)"))
		}

		renderedPath, err := RenderTemplate(targetPathRaw, evalCtx)
		if err != nil {
			return r.failStep(res, fmt.Errorf("render path template: %w", err))
		}

		targetName := renderedPath
		if strings.HasPrefix(targetName, "storage://") {
			targetName = strings.TrimPrefix(targetName, "storage://")
		}

		if err := validateStoragePath(r.storageRoot, targetName); err != nil {
			return r.failStep(res, fmt.Errorf("invalid save path: %w", err))
		}

		info, err := r.store.WriteWithBroadcast(targetName, []byte(renderedContent))
		if err != nil {
			return r.failStep(res, fmt.Errorf("write save output: %w", err))
		}

		res.Status = "completed"
		res.Output = info.Path
		res.FinishedAt = time.Now()

		if r.broadcast != nil {
			r.broadcast("storage_updated", info)
		}

		return res, nil

	case StepNotify:
		renderedMsg, err := RenderTemplate(step.Message, evalCtx)
		if err != nil {
			return r.failStep(res, fmt.Errorf("render message template: %w", err))
		}

		if r.broadcast != nil {
			r.broadcast("flow_notification", map[string]string{
				"message": renderedMsg,
			})
		}

		res.Status = "completed"
		res.Output = renderedMsg
		res.FinishedAt = time.Now()
		return res, nil

	default:
		return r.failStep(res, fmt.Errorf("unsupported step type: %s", step.Type))
	}
}

func (r *StepRunner) failStep(res StepResult, err error) (StepResult, error) {
	res.Status = "failed"
	res.Error = err.Error()
	res.FinishedAt = time.Now()
	return res, err
}
