package agents

import (
	"strings"
)

// GenerateInitialSteps returns a set of preliminary steps based on the goal.
func GenerateInitialSteps(goal string) []Step {
	goalLower := strings.ToLower(goal)
	var steps []Step

	// Rule-based estimation of steps for initial UI rendering.
	if strings.Contains(goalLower, "research") || strings.Contains(goalLower, "vector") {
		steps = []Step{
			{Number: 1, Tool: "web_search", Status: "pending", Log: "Plan to search the web for database/topic context"},
			{Number: 2, Tool: "web_fetch", Status: "pending", Log: "Plan to scrape details from top articles"},
			{Number: 3, Tool: "search_brain", Status: "pending", Log: "Plan to run local semantic search across index"},
			{Number: 4, Tool: "write_file", Status: "pending", Log: "Plan to compile and write research report"},
			{Number: 5, Tool: "notify", Status: "pending", Log: "Plan to notify the dashboard user"},
		}
	} else if strings.Contains(goalLower, "review") || strings.Contains(goalLower, "lint") {
		steps = []Step{
			{Number: 1, Tool: "list_storage", Status: "pending", Log: "Plan to scan source files"},
			{Number: 2, Tool: "run_shell", Status: "pending", Log: "Plan to run linting/testing shell tools"},
			{Number: 3, Tool: "write_file", Status: "pending", Log: "Plan to document code review summary"},
			{Number: 4, Tool: "notify", Status: "pending", Log: "Plan to notify completion"},
		}
	} else if strings.Contains(goalLower, "digest") || strings.Contains(goalLower, "summarise") || strings.Contains(goalLower, "summarize") {
		steps = []Step{
			{Number: 1, Tool: "list_storage", Status: "pending", Log: "Plan to query storage directory metadata"},
			{Number: 2, Tool: "read_file", Status: "pending", Log: "Plan to read file contents"},
			{Number: 3, Tool: "write_file", Status: "pending", Log: "Plan to draft digest file"},
			{Number: 4, Tool: "notify", Status: "pending", Log: "Plan to notify completion"},
		}
	} else if strings.Contains(goalLower, "organise") || strings.Contains(goalLower, "organizer") || strings.Contains(goalLower, "organize") {
		steps = []Step{
			{Number: 1, Tool: "list_storage", Status: "pending", Log: "Plan to read files in directory"},
			{Number: 2, Tool: "run_shell", Status: "pending", Log: "Plan to execute file movements"},
			{Number: 3, Tool: "notify", Status: "pending", Log: "Plan to notify organization completion"},
		}
	} else {
		// Generic plan
		steps = []Step{
			{Number: 1, Tool: "search_brain", Status: "pending", Log: "Plan to search context in brain RAG"},
			{Number: 2, Tool: "run_shell", Status: "pending", Log: "Plan to execute commands if required"},
			{Number: 3, Tool: "notify", Status: "pending", Log: "Plan to notify completion"},
		}
	}

	return steps
}
