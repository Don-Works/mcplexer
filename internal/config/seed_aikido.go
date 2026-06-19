package config

const (
	aikidoServerID = "aikido"

	aikidoAuthScopeID = "aikido-client-credentials"

	aikidoReadRouteID   = "aikido-read-allow"
	aikidoMutateRouteID = "aikido-mutate-approval"
)

func init() {
	RegisterEnvFields(aikidoAuthScopeID, []EnvField{
		{Key: "AIKIDO_CLIENT_ID", Label: "Client ID"},
		{Key: "AIKIDO_CLIENT_SECRET", Label: "Client Secret", Secret: true},
	})
}
