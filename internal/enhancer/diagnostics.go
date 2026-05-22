package enhancer

const (
	sectionOriginalPrompt      = "original_prompt"
	sectionTargetClient        = "target_client"
	sectionMode                = "mode"
	sectionWorkspace           = "workspace"
	sectionEnhancementContract = "enhancement_contract"
	sectionRules               = "rules"
	sectionGuidelines          = "guidelines"
	sectionHistory             = "history"
	sectionContextFiles        = "context_files"
	sectionContextRetrieval    = "context_retrieval"
	sectionFinalInstruction    = "final_instruction"
)

type SectionInfo struct {
	Name      string `json:"name"`
	Length    int    `json:"length"`
	Truncated bool   `json:"truncated"`
}
