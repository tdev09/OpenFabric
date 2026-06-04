package flow

import (
	"bytes"
	"fmt"
	"regexp"
	"text/template"
	"time"
)

var (
	// Matches "|truncate:100" -> replaces with " | truncate 100"
	truncateRegex = regexp.MustCompile(`\|\s*truncate:(\d+)`)

	// Matches "steps.XYZ.output" -> replaces with "index .steps \"XYZ\" \"output\""
	stepsOutRegex = regexp.MustCompile(`steps\.([a-zA-Z0-9_-]+)\.([a-zA-Z0-9_-]+)`)

	// Matches "trigger.filename" -> replaces with "index .trigger \"filename\""
	triggerFieldRegex = regexp.MustCompile(`trigger\.([a-zA-Z0-9_-]+)`)

	// Matches simple variables like date, time, cluster_name -> replaces with ".date", ".time", ".cluster_name"
	// but ignores keywords like index, truncate, etc.
	simpleVarRegex = regexp.MustCompile(`({{\s*)(date|datetime|time|cluster_name|node_count|pooled_ram_gb)(\s*}})`)
)

// preProcessTemplate converts user-friendly templating strings to valid Go text/template syntax.
func preProcessTemplate(tmplStr string) string {
	res := tmplStr
	// 1. Replace "|truncate:100" -> " | truncate 100"
	res = truncateRegex.ReplaceAllString(res, ` | truncate $1`)

	// 2. Replace "steps.XYZ.output" -> "index .steps \"XYZ\" \"output\""
	res = stepsOutRegex.ReplaceAllString(res, `index .steps "$1" "$2"`)

	// 3. Replace "trigger.filename" -> "index .trigger \"filename\""
	res = triggerFieldRegex.ReplaceAllString(res, `index .trigger "$1"`)

	// 4. Replace simple variables with their dotted equivalents (e.g. {{date}} -> {{.date}})
	// We do this by ensuring they are prefixed with a dot.
	res = simpleVarRegex.ReplaceAllString(res, `$1.$2$3`)

	return res
}

func truncateFunc(length interface{}, val interface{}) string {
	s := ""
	if val != nil {
		s = fmt.Sprintf("%v", val)
	}
	l := 100
	switch v := length.(type) {
	case int:
		l = v
	case int64:
		l = int(v)
	case float64:
		l = int(v)
	}
	runes := []rune(s)
	if len(runes) <= l {
		return s
	}
	return string(runes[:l]) + "..."
}

// RenderTemplate parses and executes a template string against a flow run context.
func RenderTemplate(tmplStr string, ctx map[string]interface{}) (string, error) {
	processed := preProcessTemplate(tmplStr)

	// Suppress empty triggers or steps map to prevent execution errors
	if _, ok := ctx["steps"]; !ok {
		ctx["steps"] = make(map[string]map[string]string)
	}
	if _, ok := ctx["trigger"]; !ok {
		ctx["trigger"] = make(map[string]string)
	}

	// Register helper functions
	funcMap := template.FuncMap{
		"truncate": truncateFunc,
	}

	tmpl, err := template.New("flow").Funcs(funcMap).Parse(processed)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, ctx); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// BuildTemplateContext gathers all standard template variables.
func BuildTemplateContext(clusterName string, nodeCount int, pooledRAM uint64, triggerVars map[string]string, stepsVars map[string]map[string]string) map[string]interface{} {
	now := time.Now()
	ramGB := float64(pooledRAM) / (1024 * 1024 * 1024)

	ctx := map[string]interface{}{
		"date":          now.Format("2006-01-02"),
		"datetime":      now.Format(time.RFC3339),
		"time":          now.Format("15:04:05"),
		"cluster_name":  clusterName,
		"node_count":    nodeCount,
		"pooled_ram_gb": fmt.Sprintf("%.1f", ramGB),
		"trigger":       triggerVars,
		"steps":         stepsVars,
	}

	return ctx
}


