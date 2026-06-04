package mcp

// EnvVarSpec defines a required/optional environment variable for an MCP server.
type EnvVarSpec struct {
	Key         string `json:"key"`         // e.g. "GITHUB_PERSONAL_ACCESS_TOKEN"
	Label       string `json:"label"`       // e.g. "GitHub Personal Access Token"
	Description string `json:"description"` // e.g. "Create one at github.com/settings/tokens"
	Required    bool   `json:"required"`
	Secret      bool   `json:"secret"` // rendered as password inputs in UI
}

// BuiltinServer is a pre-configured, ready-to-use integration catalog entry.
type BuiltinServer struct {
	Name        string       `json:"name"`
	Label       string       `json:"label"`
	Description string       `json:"description"`
	Icon        string       `json:"icon"` // emoji
	Command     string       `json:"command"`
	EnvVars     []EnvVarSpec `json:"env_vars"`
	DocsURL     string       `json:"docs_url"`
}

var builtinServers = []BuiltinServer{
	{
		Name:        "github",
		Label:       "GitHub",
		Description: "Connect to GitHub to view and manage issues, pull requests, and repositories.",
		Icon:        "🐙",
		Command:     "npx -y @modelcontextprotocol/server-github",
		DocsURL:     "https://github.com/modelcontextprotocol/servers/tree/main/src/github",
		EnvVars: []EnvVarSpec{
			{
				Key:         "GITHUB_PERSONAL_ACCESS_TOKEN",
				Label:       "GitHub Personal Access Token",
				Description: "Create a classic token or fine-grained token with repo scope.",
				Required:    true,
				Secret:      true,
			},
		},
	},
	{
		Name:        "notion",
		Label:       "Notion",
		Description: "Query Notion databases and search pages.",
		Icon:        "📓",
		Command:     "npx -y @modelcontextprotocol/server-notion",
		DocsURL:     "https://github.com/modelcontextprotocol/servers/tree/main/src/notion",
		EnvVars: []EnvVarSpec{
			{
				Key:         "NOTION_API_KEY",
				Label:       "Notion Integration Token",
				Description: "Create an integration token at developers.notion.com.",
				Required:    true,
				Secret:      true,
			},
		},
	},
	{
		Name:        "google-calendar",
		Label:       "Google Calendar",
		Description: "Access Google Calendar to check events, schedules, and conflicts.",
		Icon:        "📅",
		Command:     "npx -y @modelcontextprotocol/server-google-calendar",
		DocsURL:     "https://github.com/modelcontextprotocol/servers/tree/main/src/google-calendar",
		EnvVars: []EnvVarSpec{
			{
				Key:         "GOOGLE_CLIENT_ID",
				Label:       "Google Client ID",
				Description: "OAuth 2.0 Client ID from Google Cloud Console.",
				Required:    true,
				Secret:      false,
			},
			{
				Key:         "GOOGLE_CLIENT_SECRET",
				Label:       "Google Client Secret",
				Description: "OAuth 2.0 Client Secret from Google Cloud Console.",
				Required:    true,
				Secret:      true,
			},
		},
	},
	{
		Name:        "slack",
		Label:       "Slack",
		Description: "Read messages, query channels, and interact with Slack workspaces.",
		Icon:        "💬",
		Command:     "npx -y @modelcontextprotocol/server-slack",
		DocsURL:     "https://github.com/modelcontextprotocol/servers/tree/main/src/slack",
		EnvVars: []EnvVarSpec{
			{
				Key:         "SLACK_BOT_TOKEN",
				Label:       "Slack Bot Token",
				Description: "OAuth token starting with xoxb-.",
				Required:    true,
				Secret:      true,
			},
			{
				Key:         "SLACK_TEAM_ID",
				Label:       "Slack Team ID",
				Description: "The Slack workspace ID.",
				Required:    true,
				Secret:      false,
			},
		},
	},
	{
		Name:        "postgres",
		Label:       "PostgreSQL",
		Description: "Read schema, execute safe select statements on Postgres databases.",
		Icon:        "🐘",
		Command:     "npx -y @modelcontextprotocol/server-postgres",
		DocsURL:     "https://github.com/modelcontextprotocol/servers/tree/main/src/postgres",
		EnvVars: []EnvVarSpec{
			{
				Key:         "POSTGRES_CONNECTION_STRING",
				Label:       "Connection URI",
				Description: "e.g., postgresql://user:password@localhost:5432/dbname",
				Required:    true,
				Secret:      true,
			},
		},
	},
	{
		Name:        "linear",
		Label:       "Linear",
		Description: "Search issues, update status, and manage projects in Linear.",
		Icon:        "📐",
		Command:     "npx -y @modelcontextprotocol/server-linear",
		DocsURL:     "https://github.com/modelcontextprotocol/servers/tree/main/src/linear",
		EnvVars: []EnvVarSpec{
			{
				Key:         "LINEAR_API_KEY",
				Label:       "Linear API Key",
				Description: "Your personal API key from Linear Settings.",
				Required:    true,
				Secret:      true,
			},
		},
	},
	{
		Name:        "jira",
		Label:       "Jira",
		Description: "Search issues, fetch epics, and manage tickets in Jira.",
		Icon:        "🎟️",
		Command:     "npx -y @modelcontextprotocol/server-jira",
		DocsURL:     "https://github.com/modelcontextprotocol/servers/tree/main/src/jira",
		EnvVars: []EnvVarSpec{
			{
				Key:         "JIRA_URL",
				Label:       "Jira URL",
				Description: "e.g. https://your-domain.atlassian.net",
				Required:    true,
				Secret:      false,
			},
			{
				Key:         "JIRA_EMAIL",
				Label:       "Jira Account Email",
				Description: "Email address associated with your Jira account.",
				Required:    true,
				Secret:      false,
			},
			{
				Key:         "JIRA_API_TOKEN",
				Label:       "Jira API Token",
				Description: "Create an API token in your Atlassian account settings.",
				Required:    true,
				Secret:      true,
			},
		},
	},
	{
		Name:        "obsidian",
		Label:       "Obsidian",
		Description: "Read and search markdown files inside your Obsidian Vault.",
		Icon:        "💎",
		Command:     "npx -y @modelcontextprotocol/server-obsidian",
		DocsURL:     "https://github.com/modelcontextprotocol/servers/tree/main/src/obsidian",
		EnvVars: []EnvVarSpec{
			{
				Key:         "OBSIDIAN_VAULT_PATH",
				Label:       "Obsidian Vault Absolute Path",
				Description: "The absolute path to your Obsidian vault directory.",
				Required:    true,
				Secret:      false,
			},
		},
	},
	{
		Name:        "gmail",
		Label:       "Gmail",
		Description: "Read, search, and draft emails in Gmail.",
		Icon:        "✉️",
		Command:     "npx -y @modelcontextprotocol/server-gmail",
		DocsURL:     "https://github.com/modelcontextprotocol/servers/tree/main/src/gmail",
		EnvVars: []EnvVarSpec{
			{
				Key:         "GMAIL_CREDENTIALS_PATH",
				Label:       "OAuth Credentials Path",
				Description: "Path to the Google OAuth credentials JSON file.",
				Required:    true,
				Secret:      false,
			},
		},
	},
	{
		Name:        "filesystem",
		Label:       "File System",
		Description: "Access files in allowed local directories.",
		Icon:        "📁",
		Command:     "npx -y @modelcontextprotocol/server-filesystem",
		DocsURL:     "https://github.com/modelcontextprotocol/servers/tree/main/src/filesystem",
		EnvVars: []EnvVarSpec{
			{
				Key:         "ALLOWED_DIRECTORIES",
				Label:       "Allowed Directories",
				Description: "Comma-separated absolute paths that the server can access.",
				Required:    true,
				Secret:      false,
			},
		},
	},
}

// AllBuiltins returns a slice of all pre-configured integrations.
func AllBuiltins() []BuiltinServer {
	return builtinServers
}

// FindBuiltin searches for a builtin integration by name.
func FindBuiltin(name string) (BuiltinServer, bool) {
	for _, s := range builtinServers {
		if s.Name == name {
			return s, true
		}
	}
	return BuiltinServer{}, false
}
