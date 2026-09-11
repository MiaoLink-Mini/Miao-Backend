package contracts

import _ "embed"

//go:embed protocol.schema.json
var Schema []byte

//go:embed http.openapi.json
var OpenAPI []byte

//go:embed workspace-operations.json
var WorkspaceOperations []byte
