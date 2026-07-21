package config

const (
	paddleSandboxAuthScopeID    = "paddle-sandbox"
	paddleProductionAuthScopeID = "paddle-production"

	paddleServerSandboxReadID  = "paddle-sandbox-ro"
	paddleServerSandboxWriteID = "paddle-sandbox"
	paddleServerProdReadID     = "paddle-prod-ro"
	paddleServerProdWriteID    = "paddle-prod"
)

func init() {
	RegisterEnvFields(paddleSandboxAuthScopeID, []EnvField{
		{Key: "PADDLE_API_KEY", Label: "Paddle Sandbox API Key", Secret: true},
	})
	RegisterEnvFields(paddleProductionAuthScopeID, []EnvField{
		{Key: "PADDLE_API_KEY", Label: "Paddle Production API Key", Secret: true},
	})
}
