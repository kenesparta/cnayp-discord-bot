package discord

import (
	"encoding/json"
	"fmt"
)

// Intents select which events the gateway sends. They are a bitfield; see
// https://discord.com/developers/docs/topics/gateway#gateway-intents.
type Intents int

// The intents this bot may request. Privileged ones need Discord's approval.
const (
	IntentGuilds         Intents = 1 << 0
	IntentGuildMembers   Intents = 1 << 1 // privileged
	IntentGuildMessages  Intents = 1 << 9
	IntentMessageContent Intents = 1 << 15 // privileged
)

// opcode identifies the kind of gateway payload.
type opcode int

const (
	opDispatch             opcode = 0
	opHeartbeat            opcode = 1
	opIdentify             opcode = 2
	opResume               opcode = 6
	opReconnect            opcode = 7
	opInvalidSession       opcode = 9
	opHello                opcode = 10
	opHeartbeatAcknowledge opcode = 11
)

func (o opcode) String() string {
	switch o {
	case opDispatch:
		return "dispatch"
	case opHeartbeat:
		return "heartbeat"
	case opIdentify:
		return "identify"
	case opResume:
		return "resume"
	case opReconnect:
		return "reconnect"
	case opInvalidSession:
		return "invalid session"
	case opHello:
		return "hello"
	case opHeartbeatAcknowledge:
		return "heartbeat ack"
	default:
		return fmt.Sprintf("opcode(%d)", int(o))
	}
}

// Dispatch event names the bot reacts to.
const (
	eventReady             = "READY"
	eventResumed           = "RESUMED"
	eventInteractionCreate = "INTERACTION_CREATE"
)

// incoming is a gateway payload as received. The data stays raw until the
// opcode says how to interpret it.
type incoming struct {
	Op       opcode          `json:"op"`
	Data     json.RawMessage `json:"d"`
	Sequence int64           `json:"s"`
	Type     string          `json:"t"`
}

// outgoing is a gateway payload as sent. Data is always encoded, including as
// null, because the heartbeat opcode requires the field to be present.
type outgoing struct {
	Op   opcode `json:"op"`
	Data any    `json:"d"`
}

// hello carries the heartbeat interval the gateway expects us to keep.
type hello struct {
	HeartbeatIntervalMillis int `json:"heartbeat_interval"`
}

// identify opens a fresh gateway session.
type identify struct {
	Token      string             `json:"token"`
	Intents    Intents            `json:"intents"`
	Properties identifyProperties `json:"properties"`
}

type identifyProperties struct {
	OS      string `json:"os"`
	Browser string `json:"browser"`
	Device  string `json:"device"`
}

// resume continues a session that was interrupted, replaying missed events.
type resume struct {
	Token     string `json:"token"`
	SessionID string `json:"session_id"`
	Sequence  int64  `json:"seq"`
}

// A User is a Discord account, either human or bot.
type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Bot      bool   `json:"bot"`
}

// An Application is the bot's own application entry, needed to publish commands.
type Application struct {
	ID string `json:"id"`
}

// An UnavailableGuild is a guild the gateway has not sent in full yet.
type UnavailableGuild struct {
	ID          string `json:"id"`
	Unavailable bool   `json:"unavailable"`
}

// Ready is the first dispatch of a new session.
type Ready struct {
	SessionID        string             `json:"session_id"`
	ResumeGatewayURL string             `json:"resume_gateway_url"`
	User             User               `json:"user"`
	Application      Application        `json:"application"`
	Guilds           []UnavailableGuild `json:"guilds"`
}

// ApplicationCommandType distinguishes slash commands from context-menu entries.
type ApplicationCommandType int

// The command types Discord publishes.
const (
	ChatInputCommand ApplicationCommandType = 1
	UserCommand      ApplicationCommandType = 2
	MessageCommand   ApplicationCommandType = 3
)

// ApplicationCommandOptionType is the type of a single command parameter.
type ApplicationCommandOptionType int

// The option types a command parameter can declare.
const (
	SubCommandOption      ApplicationCommandOptionType = 1
	SubCommandGroupOption ApplicationCommandOptionType = 2
	StringOption          ApplicationCommandOptionType = 3
	IntegerOption         ApplicationCommandOptionType = 4
	BooleanOption         ApplicationCommandOptionType = 5
	UserOption            ApplicationCommandOptionType = 6
	ChannelOption         ApplicationCommandOptionType = 7
	RoleOption            ApplicationCommandOptionType = 8
	MentionableOption     ApplicationCommandOptionType = 9
	NumberOption          ApplicationCommandOptionType = 10
	AttachmentOption      ApplicationCommandOptionType = 11
)

// An ApplicationCommand is a command as Discord stores it.
type ApplicationCommand struct {
	ID            string                     `json:"id,omitempty"`
	ApplicationID string                     `json:"application_id,omitempty"`
	Type          ApplicationCommandType     `json:"type,omitempty"`
	Name          string                     `json:"name"`
	Description   string                     `json:"description"`
	Options       []ApplicationCommandOption `json:"options,omitempty"`
}

// An ApplicationCommandOption is one parameter of a command.
type ApplicationCommandOption struct {
	Type        ApplicationCommandOptionType `json:"type"`
	Name        string                       `json:"name"`
	Description string                       `json:"description"`
	Required    bool                         `json:"required,omitempty"`
}

// InteractionType distinguishes the ways Discord can invoke the bot.
type InteractionType int

// The ways Discord can invoke the bot.
const (
	PingInteraction               InteractionType = 1
	ApplicationCommandInteraction InteractionType = 2
	MessageComponentInteraction   InteractionType = 3
	AutocompleteInteraction       InteractionType = 4
	ModalSubmitInteraction        InteractionType = 5
)

// An Interaction is a single invocation of the bot by a user.
type Interaction struct {
	ID        string          `json:"id"`
	Token     string          `json:"token"`
	Type      InteractionType `json:"type"`
	GuildID   string          `json:"guild_id,omitempty"`
	ChannelID string          `json:"channel_id,omitempty"`
	Data      InteractionData `json:"data"`
}

// InteractionData describes which command ran and with which arguments.
type InteractionData struct {
	ID      string                  `json:"id"`
	Name    string                  `json:"name"`
	Type    ApplicationCommandType  `json:"type"`
	Options []InteractionDataOption `json:"options,omitempty"`
}

// An InteractionDataOption is one supplied argument. The value stays raw so the
// command decides how to read it.
type InteractionDataOption struct {
	Name  string                       `json:"name"`
	Type  ApplicationCommandOptionType `json:"type"`
	Value json.RawMessage              `json:"value,omitempty"`
}

// StringValue returns the option's value as a string and reports whether it was
// one. It is not named String so it does not shadow [fmt.Stringer].
func (o InteractionDataOption) StringValue() (string, bool) {
	var value string
	if err := json.Unmarshal(o.Value, &value); err != nil {
		return "", false
	}
	return value, true
}

// Option returns the named option, if the interaction carried it.
func (d InteractionData) Option(name string) (InteractionDataOption, bool) {
	for _, option := range d.Options {
		if option.Name == name {
			return option, true
		}
	}
	return InteractionDataOption{}, false
}

// InteractionResponseType selects the shape of a reply. Discord requires one
// within three seconds; use DeferredMessageResponse when the work takes longer.
type InteractionResponseType int

// The response types the bot may send.
const (
	PongResponse            InteractionResponseType = 1
	MessageResponse         InteractionResponseType = 4
	DeferredMessageResponse InteractionResponseType = 5
)

// MessageFlags modify how a message is delivered.
type MessageFlags int

// FlagEphemeral shows the message only to the user who triggered the interaction.
const FlagEphemeral MessageFlags = 1 << 6

// An InteractionResponse is the bot's reply to an interaction.
type InteractionResponse struct {
	Type InteractionResponseType  `json:"type"`
	Data *InteractionResponseData `json:"data,omitempty"`
}

// InteractionResponseData is the message body of a reply.
type InteractionResponseData struct {
	Content string       `json:"content,omitempty"`
	Flags   MessageFlags `json:"flags,omitempty"`
}

// MessageReply builds the common case: a plain visible message.
func MessageReply(content string) InteractionResponse {
	return InteractionResponse{
		Type: MessageResponse,
		Data: &InteractionResponseData{Content: content},
	}
}

// EphemeralReply builds a message only the invoking user can see.
func EphemeralReply(content string) InteractionResponse {
	return InteractionResponse{
		Type: MessageResponse,
		Data: &InteractionResponseData{Content: content, Flags: FlagEphemeral},
	}
}
