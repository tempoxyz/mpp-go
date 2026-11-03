package chargeclient

import (
	"context"
	"errors"
	"fmt"
	"strings"

	temposigner "github.com/tempoxyz/tempo-go/pkg/signer"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
)

// SubscriptionConfig configures a Tempo subscription client method.
type SubscriptionConfig struct {
	// Signer is the subscriber's root key, which signs the key authorization
	// that lets the server's access key bill each period. Any key type works:
	// secp256k1 (*signer.Signer), P256 or WebAuthn.
	Signer temposigner.EnvelopeSigner
	// PrivateKey is a hex secp256k1 root key, used when Signer is nil.
	PrivateKey string
	// AccessKey overrides the access key the Challenge names. Leave it nil to
	// authorize the key the server put in methodDetails.accessKey.
	AccessKey *tempo.SubscriptionAccessKey
	// ChainID is the chain to authorize when the Challenge names none. Zero
	// uses Tempo Moderato, as mppx does.
	ChainID int64
	// ValidateRequest, when set, runs before anything is signed and can
	// reject a subscription the subscriber does not accept, such as an
	// amount, period or expiry it does not want to authorize.
	ValidateRequest func(context.Context, tempo.SubscriptionRequest) error
}

// SubscriptionMethod creates Tempo subscription Credentials: it signs a key
// authorization scoped to the Challenge's amount, currency, recipient, period
// and expiry for the server's access key.
type SubscriptionMethod struct {
	signer          temposigner.EnvelopeSigner
	accessKey       *tempo.SubscriptionAccessKey
	chainID         int64
	validateRequest func(context.Context, tempo.SubscriptionRequest) error
}

var _ interface {
	Name() string
	Intent() string
	CreateCredential(context.Context, *mpp.Challenge) (*mpp.Credential, error)
} = (*SubscriptionMethod)(nil)

// NewSubscription constructs a Tempo subscription client method.
func NewSubscription(config SubscriptionConfig) (*SubscriptionMethod, error) {
	root := config.Signer
	if root == nil {
		if config.PrivateKey == "" {
			return nil, errors.New("tempo client: subscription signer or private key is required")
		}
		resolved, err := temposigner.NewSigner(config.PrivateKey)
		if err != nil {
			return nil, err
		}
		root = resolved
	}
	var accessKey *tempo.SubscriptionAccessKey
	if config.AccessKey != nil {
		copied := *config.AccessKey
		accessKey = &copied
	}
	return &SubscriptionMethod{
		signer:          root,
		accessKey:       accessKey,
		chainID:         config.ChainID,
		validateRequest: config.ValidateRequest,
	}, nil
}

// Name returns the method token, tempo.
func (m *SubscriptionMethod) Name() string { return tempo.MethodName }

// Intent returns the intent token, subscription.
func (m *SubscriptionMethod) Intent() string { return tempo.IntentSubscription }

// CreateCredential signs a subscription key authorization for challenge.
func (m *SubscriptionMethod) CreateCredential(ctx context.Context, challenge *mpp.Challenge) (*mpp.Credential, error) {
	if challenge == nil {
		return nil, errors.New("tempo client: challenge is required")
	}
	if challenge.Method != tempo.MethodName {
		return nil, fmt.Errorf("tempo client: unsupported challenge method %q", challenge.Method)
	}
	if challenge.Intent != tempo.IntentSubscription {
		return nil, fmt.Errorf("tempo client: unsupported challenge intent %q", challenge.Intent)
	}
	request, err := tempo.ParseSubscriptionRequest(challenge.Request)
	if err != nil {
		return nil, fmt.Errorf("tempo client: %w", err)
	}
	chainID := m.chainID
	if request.ChainID != nil {
		chainID = *request.ChainID
	}
	if chainID == 0 {
		chainID = tempo.ChainIDModerato
	}
	accessKey := m.accessKey
	if accessKey == nil {
		accessKey = request.AccessKey
	}
	if accessKey == nil {
		return nil, errors.New("tempo client: no subscription access key: the challenge must include methodDetails.accessKey, or configure SubscriptionConfig.AccessKey")
	}
	if err := tempo.ValidateSubscriptionTiming(request, ""); err != nil {
		return nil, fmt.Errorf("tempo client: %w", err)
	}
	if m.validateRequest != nil {
		if err := m.validateRequest(ctx, request); err != nil {
			return nil, err
		}
	}
	signature, err := tempo.SignSubscriptionKeyAuthorization(m.signer, *accessKey, challenge.ID, chainID, request)
	if err != nil {
		return nil, fmt.Errorf("tempo client: %w", err)
	}
	payload := tempo.SubscriptionCredentialPayload{Type: tempo.CredentialTypeKeyAuthorization, Signature: signature}
	verified, err := tempo.VerifySubscriptionKeyAuthorization(accessKey, challenge.ID, chainID, payload, request)
	if err != nil {
		return nil, fmt.Errorf("tempo client: %w", err)
	}
	if verified.Source != m.signer.Address() {
		return nil, errors.New("tempo client: keyAuthorization signer does not match the selected account")
	}
	return &mpp.Credential{
		Challenge: challenge.ToEcho(),
		Payload:   payload.Map(),
		Source:    fmt.Sprintf("did:pkh:eip155:%d:%s", chainID, strings.ToLower(m.signer.Address().Hex())),
	}, nil
}
