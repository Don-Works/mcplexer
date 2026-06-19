package config

const (
	obsidianServerID    = "obsidian"
	obsidianAuthScopeID = "obsidian-local-rest-api"
)

func init() {
	RegisterEnvFields(obsidianAuthScopeID, []EnvField{
		{Key: "Authorization", Label: "Authorization: Bearer <Obsidian API key>", Secret: true},
	})
}
