package llm

// DisplayModel holds only metadata for the UI recommendations catalogue.
type DisplayModel struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	OllamaTag     string `json:"ollama_tag"`
	RequiredRAM   int64  `json:"required_ram"` // Estimated Q4 RAM in bytes
	DefaultLayers int    `json:"default_layers"`
	DefaultHeads  int    `json:"default_heads"`
	DefaultDim    int    `json:"default_dim"`
}

// displayRegistry is the static catalogue of recommended models.
var displayRegistry = []DisplayModel{
	{
		Name:          "llama3.2:3b",
		Description:   "Meta Llama 3.2 3B - great for Pi clusters and low-RAM devices.",
		OllamaTag:     "llama3.2:3b",
		RequiredRAM:   2 * 1024 * 1024 * 1024,
		DefaultLayers: 28,
		DefaultHeads:  24,
		DefaultDim:    64,
	},
	{
		Name:          "phi3:mini",
		Description:   "Microsoft Phi-3 Mini - fast, efficient, perfect for edge clusters.",
		OllamaTag:     "phi3:mini",
		RequiredRAM:   3 * 1024 * 1024 * 1024,
		DefaultLayers: 32,
		DefaultHeads:  32,
		DefaultDim:    96,
	},
	{
		Name:          "mistral:7b",
		Description:   "Mistral 7B - excellent quality/speed balance for home clusters.",
		OllamaTag:     "mistral:7b",
		RequiredRAM:   7 * 1024 * 1024 * 1024,
		DefaultLayers: 32,
		DefaultHeads:  32,
		DefaultDim:    128,
	},
	{
		Name:          "llama3:8b",
		Description:   "Meta Llama 3 8B - strong general-purpose model, fits on most laptops.",
		OllamaTag:     "llama3:8b",
		RequiredRAM:   8 * 1024 * 1024 * 1024,
		DefaultLayers: 32,
		DefaultHeads:  32,
		DefaultDim:    128,
	},
	{
		Name:          "codellama:34b",
		Description:   "Code Llama 34B - powerful code assistant, great for developer clusters.",
		OllamaTag:     "codellama:34b",
		RequiredRAM:   20 * 1024 * 1024 * 1024,
		DefaultLayers: 48,
		DefaultHeads:  64,
		DefaultDim:    128,
	},
	{
		Name:          "mixtral:8x7b",
		Description:   "Mixtral 8x7B MoE - high quality, needs a cluster of 3+ nodes.",
		OllamaTag:     "mixtral:8x7b",
		RequiredRAM:   35 * 1024 * 1024 * 1024,
		DefaultLayers: 32,
		DefaultHeads:  32,
		DefaultDim:    128,
	},
	{
		Name:          "llama3:70b",
		Description:   "Meta Llama 3 70B - flagship quality. The hero demo model for OpenFabric.",
		OllamaTag:     "llama3:70b",
		RequiredRAM:   44 * 1024 * 1024 * 1024,
		DefaultLayers: 80,
		DefaultHeads:  64,
		DefaultDim:    128,
	},
}

// AllDisplayModels returns all models in the catalogue.
func AllDisplayModels() []DisplayModel {
	out := make([]DisplayModel, len(displayRegistry))
	copy(out, displayRegistry)
	return out
}

// FindDisplayModel retrieves a model by its tag.
func FindDisplayModel(tag string) (DisplayModel, bool) {
	for _, m := range displayRegistry {
		if m.OllamaTag == tag {
			return m, true
		}
	}
	return DisplayModel{}, false
}

// EstimatedModelInfo constructs a ModelInfo placeholder using static defaults.
func (dm DisplayModel) EstimatedModelInfo() *ModelInfo {
	return &ModelInfo{
		Name:         dm.OllamaTag,
		TotalLayers:  dm.DefaultLayers,
		TotalRAM:     dm.RequiredRAM,
		HeadCount:    dm.DefaultHeads,
		EmbedLength:  dm.DefaultHeads * dm.DefaultDim,
		IsAvailable:  false,
		Quantization: "Q4_K_M",
	}
}
