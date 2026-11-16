package discord

import "encoding/json"

// An Application is the bot's own application entry, needed to publish commands.
// [Client.CurrentApplication] reads it.
type Application struct {
	ID string `json:"id"`
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

// The flags the bot sets on the messages it sends.
const (
	// FlagSuppressEmbeds stops Discord from generating a preview card for the
	// links in a message. A list of ten events would otherwise arrive as a list
	// of ten cards.
	FlagSuppressEmbeds MessageFlags = 1 << 2

	// FlagEphemeral shows the message only to the user who triggered the
	// interaction.
	FlagEphemeral MessageFlags = 1 << 6

	// FlagSuppressNotifications delivers a message without a push or desktop
	// notification. It still appears in the channel, and still marks it unread:
	// it is the difference between a bot worth keeping unmuted and one that is
	// not.
	FlagSuppressNotifications MessageFlags = 1 << 12
)

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

// AllowedMentions says which mentions Discord may resolve in a message body.
//
// It matters because the default is permissive: a message sent without it has
// every @everyone, @here, role and user mention in its text parsed. For a
// message assembled from titles hundreds of strangers wrote, that default is
// the whole risk, and it is why [NoMentions] exists rather than a bare struct
// literal at each call site.
type AllowedMentions struct {
	// Parse is the set of mention kinds to resolve. An empty, non-nil slice
	// resolves none; a nil slice would encode as null and restore the default.
	Parse []string `json:"parse"`
}

// NoMentions resolves no mention of any kind.
func NoMentions() *AllowedMentions {
	return &AllowedMentions{Parse: []string{}}
}

// A Message is a message the bot creates in a channel.
type Message struct {
	// Content is the message body, up to 2000 characters.
	Content string `json:"content,omitempty"`

	// Flags modify delivery; see [FlagSuppressEmbeds] and
	// [FlagSuppressNotifications].
	Flags MessageFlags `json:"flags,omitempty"`

	// AllowedMentions is never omitted in practice: [Client.CreateMessage] fills
	// it with [NoMentions] when a caller leaves it nil.
	AllowedMentions *AllowedMentions `json:"allowed_mentions,omitempty"`
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
