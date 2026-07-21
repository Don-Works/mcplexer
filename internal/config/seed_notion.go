package config

const notionAuthScopeID = "notion-token"

func init() {
	RegisterEnvFields(notionAuthScopeID, []EnvField{
		{Key: "NOTION_TOKEN", Label: "Integration Token", Secret: true},
	})
}
