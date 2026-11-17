package agent

import (
	"context"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

// Host contains the application's live resolvers. It is assembled before agents
// are published and remains immutable; individual callbacks may read settings
// at run time. A nil Host uses built-in prompts and tools without host features.
type Host struct {
	PromptOverride                 func(string) (string, bool)
	OpsecEnabled                   func() bool
	WrapupOverride                 func(string) (string, bool)
	WrapupMaxTurnsOverride         func(string) (int, bool)
	WrapupTaskTimeoutOverride      func(string) (string, bool)
	WrapupTaskTimeoutTurnsOverride func(string) (int, bool)
	ToolAugment                    func(context.Context, string) ([]actool.CoreTool, DeferredInfo, func())
	ToolResolve                    func(context.Context, string, []actool.CoreTool) []actool.CoreTool
	FindingTrafficBindingEnabled   func() bool
	IntelligenceEnabled            func() bool
	IntelligenceRecord             func(context.Context, int64, int64, string, db.IntelligenceInput) (int64, error)
	CredentialRecorded             func()
}
