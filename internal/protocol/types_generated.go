// Code generated from contracts/protocol.schema.json. DO NOT EDIT.
package protocol

import "encoding/json"

type ID string
type Timestamp string
type Sequence int64
type Prompt string
type Text string
type OpaqueToken string
type Capabilities struct {
	Send              bool     `json:"send"`
	Cancel            bool     `json:"cancel"`
	Approval          bool     `json:"approval"`
	Question          bool     `json:"question"`
	Diff              bool     `json:"diff"`
	Plan              bool     `json:"plan"`
	Usage             bool     `json:"usage"`
	Queue             bool     `json:"queue"`
	Steer             bool     `json:"steer"`
	Models            bool     `json:"models,omitempty"`
	Compact           bool     `json:"compact,omitempty"`
	Workspace         bool     `json:"workspace,omitempty"`
	HistoryImport     bool     `json:"historyImport,omitempty"`
	WorkspaceSections []string `json:"workspaceSections,omitempty"`
}
type SessionState string
type ErrorCode string
type Error struct {
	Code      ErrorCode `json:"code"`
	Message   string    `json:"message"`
	Retryable bool      `json:"retryable"`
	RequestID ID        `json:"requestId"`
}
type ErrorResponse struct {
	Error Error `json:"error"`
}
type User struct {
	ID          ID        `json:"id"`
	DisplayName string    `json:"displayName"`
	CreatedAt   Timestamp `json:"createdAt"`
}
type Node struct {
	ID       ID         `json:"id"`
	Name     string     `json:"name"`
	Platform string     `json:"platform"`
	Version  string     `json:"version"`
	Online   bool       `json:"online"`
	LastSeen *Timestamp `json:"lastSeen"`
	Revoked  bool       `json:"revoked"`
	Revision Sequence   `json:"revision"`
}
type Project struct {
	ID          ID       `json:"id"`
	NodeID      ID       `json:"nodeId"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Branch      *string  `json:"branch"`
	Valid       bool     `json:"valid"`
	Revision    Sequence `json:"revision"`
}
type Agent struct {
	ID                 ID           `json:"id"`
	NodeID             ID           `json:"nodeId"`
	Name               string       `json:"name"`
	State              string       `json:"state"`
	Version            *string      `json:"version"`
	AdapterVersion     string       `json:"adapterVersion"`
	Capabilities       Capabilities `json:"capabilities"`
	CapabilityRevision Sequence     `json:"capabilityRevision"`
	Revision           Sequence     `json:"revision"`
}
type QueueItem struct {
	ID          ID        `json:"id"`
	OperationID ID        `json:"operationId"`
	Text        Prompt    `json:"text"`
	State       string    `json:"state"`
	CreatedAt   Timestamp `json:"createdAt"`
}
type Session struct {
	ID                 ID           `json:"id"`
	NodeID             ID           `json:"nodeId"`
	ProjectID          ID           `json:"projectId"`
	AgentID            ID           `json:"agentId"`
	Title              string       `json:"title"`
	State              SessionState `json:"state"`
	TurnID             *ID          `json:"turnId"`
	Mode               string       `json:"mode"`
	Capabilities       Capabilities `json:"capabilities"`
	CapabilityRevision Sequence     `json:"capabilityRevision"`
	Queue              []QueueItem  `json:"queue"`
	CreatedAt          Timestamp    `json:"createdAt"`
	UpdatedAt          Timestamp    `json:"updatedAt"`
	Revision           Sequence     `json:"revision"`
	LastSequence       Sequence     `json:"lastSequence"`
	HistoryState       string       `json:"historyState"`
	Pinned             bool         `json:"pinned,omitempty"`
	Tags               []string     `json:"tags,omitempty"`
	Archived           bool         `json:"archived,omitempty"`
	ParentSessionID    *ID          `json:"parentSessionId,omitempty"`
	OriginMode         string       `json:"originMode,omitempty"`
	OriginPoint        *string      `json:"originPoint,omitempty"`
}
type Choice struct {
	ID    ID     `json:"id"`
	Label string `json:"label"`
}
type Question json.RawMessage
type Answers map[string]json.RawMessage
type Decision json.RawMessage
type InteractionRequest json.RawMessage
type Notification struct {
	ID        ID        `json:"id"`
	SessionID *ID       `json:"sessionId"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Summary   string    `json:"summary"`
	Read      bool      `json:"read"`
	CreatedAt Timestamp `json:"createdAt"`
	Revision  Sequence  `json:"revision"`
}
type Audit struct {
	ID          ID        `json:"id"`
	Title       string    `json:"title"`
	Action      string    `json:"action"`
	State       string    `json:"state"`
	OperationID *ID       `json:"operationId"`
	NodeID      *ID       `json:"nodeId"`
	SessionID   *ID       `json:"sessionId"`
	CreatedAt   Timestamp `json:"createdAt"`
}
type OperationResult struct {
	SessionID   *ID          `json:"sessionId"`
	TurnID      *ID          `json:"turnId"`
	RequestID   *ID          `json:"requestId"`
	NodeID      *ID          `json:"nodeId"`
	QueueItemID *ID          `json:"queueItemId"`
	Native      NativeResult `json:"native,omitempty"`
}
type Operation struct {
	ID         ID               `json:"id"`
	Kind       string           `json:"kind"`
	State      string           `json:"state"`
	CreatedAt  Timestamp        `json:"createdAt"`
	UpdatedAt  Timestamp        `json:"updatedAt"`
	DeadlineAt Timestamp        `json:"deadlineAt"`
	Result     *OperationResult `json:"result"`
	Error      *Error           `json:"error"`
	Revision   Sequence         `json:"revision"`
}
type CreateSession struct {
	NodeID             ID            `json:"nodeId"`
	ProjectID          ID            `json:"projectId"`
	AgentID            ID            `json:"agentId"`
	Prompt             Prompt        `json:"prompt,omitempty"`
	CapabilityRevision Sequence      `json:"capabilityRevision"`
	Origin             SessionOrigin `json:"origin,omitempty"`
	Preset             SessionPreset `json:"preset,omitempty"`
	HistoryOnly        string        `json:"historyOnly,omitempty"`
}
type SendMessage struct {
	ExpectedTurnID     ID       `json:"expectedTurnId"`
	Text               Prompt   `json:"text"`
	Mode               string   `json:"mode"`
	CapabilityRevision Sequence `json:"capabilityRevision"`
	Attachments        []ID     `json:"attachments,omitempty"`
	References         []ID     `json:"references,omitempty"`
	CommandID          ID       `json:"commandId,omitempty"`
}
type CancelTurn struct {
	ExpectedTurnID     ID       `json:"expectedTurnId"`
	CapabilityRevision Sequence `json:"capabilityRevision"`
}
type RespondRequest struct {
	ExpectedTurnID     ID       `json:"expectedTurnId"`
	RequestRevision    Sequence `json:"requestRevision"`
	CapabilityRevision Sequence `json:"capabilityRevision"`
	Decision           Decision `json:"decision"`
}
type Empty struct {
}
type Login struct {
	Code string `json:"code"`
}
type LoginResult struct {
	AccessToken OpaqueToken `json:"accessToken"`
	ExpiresAt   Timestamp   `json:"expiresAt"`
	User        User        `json:"user"`
}
type PairPreviewInput struct {
	Code string `json:"code"`
}
type PairPreview struct {
	TicketID       ID        `json:"ticketId"`
	NodeName       string    `json:"nodeName"`
	Platform       string    `json:"platform"`
	KeyFingerprint string    `json:"keyFingerprint"`
	ExpiresAt      Timestamp `json:"expiresAt"`
}
type PairConfirm struct {
	TicketID ID `json:"ticketId"`
}
type NodesPage struct {
	Items         []Node  `json:"items"`
	NextPageToken *string `json:"nextPageToken"`
}
type ProjectsPage struct {
	Items         []Project `json:"items"`
	NextPageToken *string   `json:"nextPageToken"`
}
type AgentsPage struct {
	Items         []Agent `json:"items"`
	NextPageToken *string `json:"nextPageToken"`
}
type SessionsPage struct {
	Items         []Session `json:"items"`
	NextPageToken *string   `json:"nextPageToken"`
}
type RequestsPage struct {
	Items         []InteractionRequest `json:"items"`
	NextPageToken *string              `json:"nextPageToken"`
}
type NotificationsPage struct {
	Items         []Notification `json:"items"`
	NextPageToken *string        `json:"nextPageToken"`
}
type AuditsPage struct {
	Items         []Audit `json:"items"`
	NextPageToken *string `json:"nextPageToken"`
}
type Bootstrap struct {
	User       User      `json:"user"`
	Revision   Sequence  `json:"revision"`
	ServerTime Timestamp `json:"serverTime"`
	Counts     struct {
		OnlineNodes         int64 `json:"onlineNodes"`
		Sessions            int64 `json:"sessions"`
		PendingRequests     int64 `json:"pendingRequests"`
		UnreadNotifications int64 `json:"unreadNotifications"`
	} `json:"counts"`
	Nodes         NodesPage         `json:"nodes"`
	Projects      ProjectsPage      `json:"projects"`
	Agents        AgentsPage        `json:"agents"`
	Sessions      SessionsPage      `json:"sessions"`
	Requests      RequestsPage      `json:"requests"`
	Notifications NotificationsPage `json:"notifications"`
	Audit         AuditsPage        `json:"audit"`
}
type FileSummary struct {
	FileID  ID          `json:"fileId"`
	Path    DisplayPath `json:"path"`
	Added   int64       `json:"added"`
	Removed int64       `json:"removed"`
}
type DiffFile struct {
	ID        ID          `json:"id"`
	SessionID ID          `json:"sessionId"`
	ItemID    ID          `json:"itemId"`
	Path      DisplayPath `json:"path"`
	Language  *string     `json:"language"`
	Patch     Text        `json:"patch"`
	Truncated bool        `json:"truncated"`
	CreatedAt Timestamp   `json:"createdAt"`
}
type TimelineItem json.RawMessage
type SessionEvent json.RawMessage
type NodeEvent json.RawMessage
type EventsPage struct {
	SessionID        ID             `json:"sessionId"`
	After            Sequence       `json:"after"`
	HighWater        Sequence       `json:"highWater"`
	Events           []SessionEvent `json:"events"`
	NextAfter        Sequence       `json:"nextAfter"`
	HasMore          bool           `json:"hasMore"`
	EarliestSequence int64          `json:"earliestSequence"`
}
type Checkpoint struct {
	ID        ID        `json:"id"`
	SessionID ID        `json:"sessionId"`
	Sequence  Sequence  `json:"sequence"`
	CreatedAt Timestamp `json:"createdAt"`
	ItemCount int64     `json:"itemCount"`
	ExpiresAt Timestamp `json:"expiresAt"`
}
type CheckpointItemsPage struct {
	CheckpointID  ID             `json:"checkpointId"`
	Sequence      Sequence       `json:"sequence"`
	Items         []TimelineItem `json:"items"`
	NextPageToken *string        `json:"nextPageToken"`
}
type ResourceChange struct {
	Revision         int64    `json:"revision"`
	ResourceType     string   `json:"resourceType"`
	ResourceID       ID       `json:"resourceId"`
	Action           string   `json:"action"`
	ResourceRevision Sequence `json:"resourceRevision"`
}
type ChangesPage struct {
	After     Sequence         `json:"after"`
	HighWater Sequence         `json:"highWater"`
	Changes   []ResourceChange `json:"changes"`
	NextAfter Sequence         `json:"nextAfter"`
	HasMore   bool             `json:"hasMore"`
}
type Health struct {
	Status string `json:"status"`
}
type NodeCommand json.RawMessage
type NodeSessionState struct {
	SessionID          ID           `json:"sessionId"`
	TurnID             *ID          `json:"turnId"`
	State              SessionState `json:"state"`
	Capabilities       Capabilities `json:"capabilities"`
	CapabilityRevision Sequence     `json:"capabilityRevision"`
	Queue              []QueueItem  `json:"queue"`
}
type EnrollmentInput struct {
	PublicKey string `json:"publicKey"`
	Name      string `json:"name"`
	Platform  string `json:"platform"`
	Version   string `json:"version"`
}
type Enrollment struct {
	ID             ID          `json:"id"`
	Code           string      `json:"code"`
	PollToken      OpaqueToken `json:"pollToken"`
	KeyFingerprint string      `json:"keyFingerprint"`
	ExpiresAt      Timestamp   `json:"expiresAt"`
}
type EnrollmentStatus struct {
	State     string    `json:"state"`
	NodeID    *ID       `json:"nodeId"`
	ExpiresAt Timestamp `json:"expiresAt"`
}
type NodeChallengeInput struct {
	NodeID ID `json:"nodeId"`
}
type NodeChallenge struct {
	ID           ID        `json:"id"`
	SigningInput string    `json:"signingInput"`
	ExpiresAt    Timestamp `json:"expiresAt"`
}
type NodeProof struct {
	ChallengeID ID     `json:"challengeId"`
	Signature   string `json:"signature"`
}
type NodeToken struct {
	AccessToken OpaqueToken `json:"accessToken"`
	ExpiresAt   Timestamp   `json:"expiresAt"`
}
type ClientFrame json.RawMessage
type ServerFrame json.RawMessage
type NodeToGatewayFrame json.RawMessage
type GatewayToNodeFrame json.RawMessage
type NodeProject struct {
	ID          ID      `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Branch      *string `json:"branch"`
	Valid       bool    `json:"valid"`
}
type NodeAgent struct {
	ID                 ID           `json:"id"`
	Name               string       `json:"name"`
	State              string       `json:"state"`
	Version            *string      `json:"version"`
	AdapterVersion     string       `json:"adapterVersion"`
	Capabilities       Capabilities `json:"capabilities"`
	CapabilityRevision Sequence     `json:"capabilityRevision"`
}
type NodeRequest json.RawMessage
type DisplayPath string
type NativeAction json.RawMessage
type NativeControl struct {
	ExpectedTurnID     ID           `json:"expectedTurnId"`
	CapabilityRevision Sequence     `json:"capabilityRevision"`
	Control            NativeAction `json:"control"`
}
type NativeModel struct {
	ID            string   `json:"id"`
	Label         string   `json:"label"`
	Input         []string `json:"input,omitempty"`
	Thinking      []string `json:"thinking,omitempty"`
	InputEvidence string   `json:"inputEvidence,omitempty"`
}
type NativeResult json.RawMessage
type WorkspaceRequest json.RawMessage
type WorkspaceField struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Type    string `json:"type"`
	Options []struct {
		Value string `json:"value"`
		Label string `json:"label"`
	} `json:"options,omitempty"`
	MaxLength int64 `json:"maxLength,omitempty"`
}
type WorkspaceControl struct {
	ID      ID               `json:"id"`
	Label   string           `json:"label"`
	Request WorkspaceRequest `json:"request"`
	Fields  []WorkspaceField `json:"fields,omitempty"`
	Confirm bool             `json:"confirm"`
	Notice  string           `json:"notice,omitempty"`
}
type SessionOrigin struct {
	SessionID       ID     `json:"sessionId"`
	SourceSessionID ID     `json:"sourceSessionId,omitempty"`
	ResourceID      ID     `json:"resourceId"`
	Mode            string `json:"mode"`
	PointID         ID     `json:"pointId,omitempty"`
	PointLabel      string `json:"pointLabel,omitempty"`
	ProjectHistory  string `json:"projectHistory,omitempty"`
}
type SessionPreset struct {
	SessionID  ID `json:"sessionId"`
	ResourceID ID `json:"resourceId"`
}
type WorkspaceEntry struct {
	ID          ID                 `json:"id"`
	Label       string             `json:"label"`
	Detail      string             `json:"detail,omitempty"`
	State       string             `json:"state,omitempty"`
	Request     WorkspaceRequest   `json:"request,omitempty"`
	Controls    []WorkspaceControl `json:"controls,omitempty"`
	ReferenceID ID                 `json:"referenceId,omitempty"`
	Origin      SessionOrigin      `json:"origin,omitempty"`
	Preset      SessionPreset      `json:"preset,omitempty"`
	Version     string             `json:"version,omitempty"`
	MediaType   string             `json:"mediaType,omitempty"`
	Size        int64              `json:"size,omitempty"`
}
type WorkspaceTransfer struct {
	ID         ID        `json:"id"`
	Name       string    `json:"name"`
	MediaType  string    `json:"mediaType"`
	Size       int64     `json:"size"`
	Sha256     string    `json:"sha256,omitempty"`
	Offset     int64     `json:"offset"`
	NextOffset int64     `json:"nextOffset,omitempty"`
	Content    string    `json:"content,omitempty"`
	State      string    `json:"state"`
	ExpiresAt  Timestamp `json:"expiresAt,omitempty"`
	Version    string    `json:"version,omitempty"`
	Eof        bool      `json:"eof,omitempty"`
}
type WorkspaceView struct {
	Title       string             `json:"title"`
	Notice      string             `json:"notice"`
	Entries     []WorkspaceEntry   `json:"entries"`
	Controls    []WorkspaceControl `json:"controls"`
	Text        string             `json:"text,omitempty"`
	Transfer    WorkspaceTransfer  `json:"transfer,omitempty"`
	ExternalUrl string             `json:"externalUrl,omitempty"`
	Prefill     string             `json:"prefill,omitempty"`
	RequestKind string             `json:"requestKind,omitempty"`
	QueuePolicy string             `json:"queuePolicy,omitempty"`
	Truncated   bool               `json:"truncated,omitempty"`
}
type SessionMetadata struct {
	Revision Sequence `json:"revision"`
	Title    string   `json:"title,omitempty"`
	Pinned   bool     `json:"pinned,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Archived bool     `json:"archived,omitempty"`
}
type DeleteHistory struct {
	Revision           Sequence `json:"revision"`
	Scope              string   `json:"scope"`
	IncludeDescendants bool     `json:"includeDescendants"`
}
type HistoryExport struct {
	Format     string    `json:"format"`
	ExportedAt Timestamp `json:"exportedAt"`
	Session    struct {
		Title           string `json:"title"`
		Agent           string `json:"agent"`
		ParentSessionID *ID    `json:"parentSessionId"`
	} `json:"session"`
	Items []struct {
		Role      string    `json:"role"`
		Text      string    `json:"text"`
		CreatedAt Timestamp `json:"createdAt,omitempty"`
	} `json:"items"`
	Truncated bool `json:"truncated"`
}
type ImportHistory struct {
	NodeID     ID            `json:"nodeId"`
	ProjectID  ID            `json:"projectId"`
	AgentID    ID            `json:"agentId"`
	Transcript HistoryExport `json:"transcript"`
}
type SubscriptionTemplate struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Kind  string `json:"kind"`
}
type SubscriptionConfig struct {
	Enabled   bool                   `json:"enabled"`
	Templates []SubscriptionTemplate `json:"templates"`
	Notice    string                 `json:"notice"`
}
type SubscriptionConsent struct {
	Choices []struct {
		TemplateID string `json:"templateId"`
		Decision   string `json:"decision"`
	} `json:"choices"`
	Nonce ID `json:"nonce"`
}
type SharePermission string
type CreateShare struct {
	Permission SharePermission `json:"permission"`
	TtlSeconds int64           `json:"ttlSeconds"`
}
type ShareToken string
type RedeemShare struct {
	Token ShareToken `json:"token"`
}
type SessionShare struct {
	ID            ID              `json:"id"`
	SessionID     ID              `json:"sessionId"`
	Title         string          `json:"title"`
	Permission    SharePermission `json:"permission"`
	CreatedAt     Timestamp       `json:"createdAt"`
	ExpiresAt     Timestamp       `json:"expiresAt"`
	RevokedAt     *Timestamp      `json:"revokedAt"`
	AcceptedAt    *Timestamp      `json:"acceptedAt"`
	RecipientName *string         `json:"recipientName"`
}
type ShareInvitation struct {
	Share      SessionShare `json:"share"`
	Token      *ShareToken  `json:"token"`
	ServerTime Timestamp    `json:"serverTime"`
}
type ShareList struct {
	Items         []SessionShare `json:"items"`
	NextPageToken *string        `json:"nextPageToken"`
	ServerTime    Timestamp      `json:"serverTime"`
}
type SharedSession struct {
	Share       SessionShare         `json:"share"`
	Session     Session              `json:"session"`
	OwnerName   string               `json:"ownerName"`
	AgentName   string               `json:"agentName"`
	ProjectName string               `json:"projectName"`
	NodeName    string               `json:"nodeName"`
	NodeOnline  bool                 `json:"nodeOnline"`
	Requests    []InteractionRequest `json:"requests"`
	ServerTime  Timestamp            `json:"serverTime"`
}
type SharedCommand json.RawMessage
type ShareActivity struct {
	Items []struct {
		ID        ID        `json:"id"`
		ShareID   ID        `json:"shareId"`
		ActorName string    `json:"actorName"`
		Action    string    `json:"action"`
		State     string    `json:"state"`
		CreatedAt Timestamp `json:"createdAt"`
	} `json:"items"`
	NextPageToken *string `json:"nextPageToken"`
}
type UserProfile struct {
	DisplayName  string  `json:"displayName"`
	AvatarBase64 *string `json:"avatarBase64"`
	Revision     int64   `json:"revision"`
}
type UpdateUserProfile struct {
	DisplayName  string  `json:"displayName"`
	AvatarBase64 *string `json:"avatarBase64,omitempty"`
	Revision     int64   `json:"revision"`
}
type ProjectHistory struct {
	NodeID             ID                         `json:"nodeId"`
	ProjectID          ID                         `json:"projectId"`
	AgentID            ID                         `json:"agentId"`
	CapabilityRevision Sequence                   `json:"capabilityRevision"`
	Request            map[string]json.RawMessage `json:"request"`
}
