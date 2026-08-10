package lspapi

import (
	"errors"
	"fmt"
	"log"
	"math"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAPayInboundInvoiceExpiry            time.Duration = 3600 * time.Second
	defaultAPayOutboundInvoiceExpiry           time.Duration = 900 * time.Second
	defaultAPayInboundMinFinalCltvExpiryDelta  uint16        = 144
	defaultAPayOutboundMinFinalCltvExpiryDelta uint16        = 42
	defaultAPayClaimMarginBlocks               uint32        = 12

	// LDK protocol constants: MIN_FINAL_CLTV_EXPIRY_DELTA = ldkHtlcFailBackBuffer + ldkMinFinalCltvBuffer = 42
	ldkHtlcFailBackBuffer = 39 // LDK subtracts it in PaymentClaimable.claim_deadline
	ldkMinFinalCltvBuffer = 3  // create_bolt11_invoice adds it to the requested min_final_cltv_expiry_delta
)

type Config struct {
	ServerAddr string

	DatabaseDriver string
	DatabaseURL    string

	LSPBaseURL              string
	LSPToken                string
	RGBNodeBaseURL          string
	RGBNodeToken            string
	HTTPTimeout             time.Duration
	CronEvery               time.Duration
	SendRGBFeeRate          uint64
	MinConfirmations        uint8
	ExpiryMatchToleranceSec uint32
	MinAmtMsat              uint64
	DefaultRGBAssignment    string

	LightningAddressDomainURL           string
	LightningAddressShortDescription    string
	LightningAddressMinSendableMsat     uint64
	LightningAddressMaxSendableMsat     uint64
	APayBearerToken                     string
	APayInboundInvoiceExpiry            time.Duration
	APayOutboundInvoiceExpiry           time.Duration
	APayInboundMinFinalCltvExpiryDelta  uint16
	APayOutboundMinFinalCltvExpiryDelta uint16
	APayClaimMarginBlocks               uint32

	DefaultChannelCapacitySat uint64
	DefaultChannelAssetAmount uint64
	DefaultChannelPushMsat    uint64

	// Cadence only — retries stop at the RGB invoice expiry, not here.
	DeliveryRetryBaseDelay time.Duration
	DeliveryRetryMaxDelay  time.Duration
	// Mirrors the peer's max_inbound_htlc_value_in_flight_percent (RLN default 10),
	// to derive the per-HTLC ceiling for channels that do not exist yet.
	PeerInFlightPercent    uint64
	SupportedAssetIDs      []string
	DefaultVirtualOpenMode string
	// Node P2P address for GET /get_info: the node never reports it back.
	LSPNodeHost      string
	LSPNodePort      int
	GetInfoAssetsTTL time.Duration
	UtxoMinCount     uint32
	UtxoTargetCount  uint32
	UtxoSizeSat      uint32
	UtxoFeeRate      uint64
	UtxoSkipSync     bool

	AdminHandles map[string]string // e.g. "admin": "<compressed_pubkey_hex>"
}

func LoadConfig() Config {
	cfg := Config{
		ServerAddr:                       envOrDefault("SERVER_ADDR", ":8080"),
		DatabaseDriver:                   envOrDefault("DATABASE_DRIVER", "sqlite"),
		DatabaseURL:                      envOrDefault("DATABASE_URL", "utexo_lsp.db"),
		LSPBaseURL:                       strings.TrimRight(envOrDefault("LSP_BASE_URL", "http://127.0.0.1:3001"), "/"),
		LSPToken:                         os.Getenv("LSP_TOKEN"),
		RGBNodeBaseURL:                   strings.TrimRight(envOrDefault("RGB_NODE_BASE_URL", envOrDefault("LSP_BASE_URL", "http://127.0.0.1:3001")), "/"),
		RGBNodeToken:                     os.Getenv("RGB_NODE_TOKEN"),
		HTTPTimeout:                      durationOrDefault("HTTP_TIMEOUT", 15*time.Second),
		CronEvery:                        durationOrDefault("CRON_EVERY", 30*time.Second),
		SendRGBFeeRate:                   uint64(intOrDefault("SENDRGB_FEE_RATE", 1)),
		MinConfirmations:                 uint8(intOrDefault("MIN_CONFIRMATIONS", 1)),
		ExpiryMatchToleranceSec:          uint32(intOrDefault("EXPIRY_MATCH_TOLERANCE_SEC", 5)),
		MinAmtMsat:                       uint64(intOrDefault("MIN_AMT_MSAT", 3_000_000)),
		DefaultRGBAssignment:             envOrDefault("DEFAULT_RGB_ASSIGNMENT", "Any"),
		LightningAddressDomainURL:        envOrDefault("LIGHTNING_ADDRESS_DOMAIN_URL", "http://127.0.0.1:8080"),
		LightningAddressShortDescription: envOrDefault("LIGHTNING_ADDRESS_SHORT_DESCRIPTION", "Payment to utexo-lsp"),
		LightningAddressMinSendableMsat:  uint64(intOrDefault("LIGHTNING_ADDRESS_MIN_SENDABLE_MSAT", 3_000_000)),
		LightningAddressMaxSendableMsat:  uint64(intOrDefault("LIGHTNING_ADDRESS_MAX_SENDABLE_MSAT", 3_000_000)),

		APayInboundInvoiceExpiry:            durationOrDefault("APAY_INBOUND_INVOICE_EXPIRY", defaultAPayInboundInvoiceExpiry),
		APayOutboundInvoiceExpiry:           durationOrDefault("APAY_OUTBOUND_INVOICE_EXPIRY", defaultAPayOutboundInvoiceExpiry),
		APayInboundMinFinalCltvExpiryDelta:  uint16OrDefault("APAY_INBOUND_MIN_FINAL_CLTV_EXPIRY_DELTA", defaultAPayInboundMinFinalCltvExpiryDelta),
		APayOutboundMinFinalCltvExpiryDelta: uint16OrDefault("APAY_OUTBOUND_MIN_FINAL_CLTV_EXPIRY_DELTA", defaultAPayOutboundMinFinalCltvExpiryDelta),
		APayClaimMarginBlocks:               uint32OrDefault("APAY_CLAIM_MARGIN_BLOCKS", defaultAPayClaimMarginBlocks),
		APayBearerToken:                     os.Getenv("APAY_BEARER_TOKEN"),

		DefaultChannelCapacitySat: uint64(intOrDefault("DEFAULT_CHANNEL_CAPACITY_SAT", 200000)),
		DefaultChannelAssetAmount: uint64(intOrDefault("DEFAULT_CHANNEL_ASSET_AMOUNT", 1)),
		DefaultChannelPushMsat:    uint64(intOrDefault("DEFAULT_CHANNEL_PUSH_MSAT", 0)),
		DeliveryRetryBaseDelay:    durationOrDefault("DELIVERY_RETRY_BASE_DELAY", 30*time.Second),
		DeliveryRetryMaxDelay:     durationOrDefault("DELIVERY_RETRY_MAX_DELAY", 5*time.Minute),
		PeerInFlightPercent:       uint64(intOrDefault("PEER_MAX_INBOUND_HTLC_IN_FLIGHT_PERCENT", 10)),
		SupportedAssetIDs:         csvOrDefault("SUPPORTED_ASSET_IDS", ""),
		DefaultVirtualOpenMode:    strings.TrimSpace(os.Getenv("DEFAULT_VIRTUAL_OPEN_MODE")),
		LSPNodeHost:               strings.TrimSpace(os.Getenv("LSP_NODE_HOST")),
		LSPNodePort:               intOrDefault("LSP_NODE_PORT", 0),
		GetInfoAssetsTTL:          durationOrDefault("GET_INFO_ASSETS_TTL", 5*time.Minute),
		UtxoMinCount:              uint32(intOrDefault("UTXO_MIN_COUNT", 0)),
		UtxoTargetCount:           uint32(intOrDefault("UTXO_TARGET_COUNT", 0)),
		UtxoSizeSat:               uint32(intOrDefault("UTXO_SIZE_SAT", 32000)),
		UtxoFeeRate:               uint64(intOrDefault("UTXO_FEE_RATE", 1)),
		UtxoSkipSync:              boolOrDefault("UTXO_SKIP_SYNC", false),
	}

	if cfg.LightningAddressMinSendableMsat < cfg.MinAmtMsat {
		cfg.LightningAddressMinSendableMsat = cfg.MinAmtMsat
	}
	if cfg.LightningAddressMaxSendableMsat < cfg.MinAmtMsat {
		cfg.LightningAddressMaxSendableMsat = cfg.MinAmtMsat
	}
	if cfg.APayInboundInvoiceExpiry <= 0 {
		cfg.APayInboundInvoiceExpiry = defaultAPayInboundInvoiceExpiry
	}
	if cfg.APayOutboundInvoiceExpiry <= 0 {
		cfg.APayOutboundInvoiceExpiry = defaultAPayOutboundInvoiceExpiry
	}
	if cfg.APayInboundMinFinalCltvExpiryDelta == 0 {
		cfg.APayInboundMinFinalCltvExpiryDelta = defaultAPayInboundMinFinalCltvExpiryDelta
	}
	if cfg.APayOutboundMinFinalCltvExpiryDelta == 0 {
		cfg.APayOutboundMinFinalCltvExpiryDelta = defaultAPayOutboundMinFinalCltvExpiryDelta
	}
	if cfg.APayClaimMarginBlocks == 0 {
		cfg.APayClaimMarginBlocks = defaultAPayClaimMarginBlocks
	}
	if cfg.DeliveryRetryBaseDelay <= 0 {
		cfg.DeliveryRetryBaseDelay = 30 * time.Second
	}
	if cfg.DeliveryRetryMaxDelay < cfg.DeliveryRetryBaseDelay {
		cfg.DeliveryRetryMaxDelay = cfg.DeliveryRetryBaseDelay
	}
	if cfg.PeerInFlightPercent == 0 || cfg.PeerInFlightPercent > 100 {
		cfg.PeerInFlightPercent = 10
	}
	if cfg.LSPBaseURL == "" {
		log.Fatal("LSP_BASE_URL is required")
	}

	adminHandlesStr := os.Getenv("ADMIN_HANDLES")
	adminHandles := make(map[string]string)
	if strings.TrimSpace(adminHandlesStr) != "" {
		for _, pair := range strings.Split(adminHandlesStr, ",") {
			parts := strings.Split(strings.TrimSpace(pair), ":")
			if len(parts) == 2 {
				username := strings.ToLower(strings.TrimSpace(parts[0]))
				pubkey := strings.ToLower(strings.TrimSpace(parts[1]))
				if username != "" && pubkey != "" {
					adminHandles[username] = pubkey
				}
			}
		}
	}
	cfg.AdminHandles = adminHandles

	return cfg
}

// maxDeliverableMsat estimates the per-HTLC ceiling of a channel we have not opened
// yet. For an existing one prefer the node's next_outbound_htlc_limit_msat.
func (cfg Config) maxDeliverableMsat() uint64 {
	return cfg.DefaultChannelCapacitySat * 1000 * cfg.PeerInFlightPercent / 100
}

func (cfg Config) Validate() error {
	if cfg.LSPBaseURL == "" {
		return errors.New("LSP_BASE_URL is required")
	}
	if cfg.LightningAddressDomainURL == "" {
		return errors.New("LIGHTNING_ADDRESS_DOMAIN_URL is required")
	}
	if err := validateLightningAddressDomainURL(cfg.LightningAddressDomainURL); err != nil {
		return fmt.Errorf("invalid LIGHTNING_ADDRESS_DOMAIN_URL: %w", err)
	}
	if cfg.LightningAddressMaxSendableMsat < cfg.LightningAddressMinSendableMsat {
		return errors.New("LIGHTNING_ADDRESS_MAX_SENDABLE_MSAT must be >= LIGHTNING_ADDRESS_MIN_SENDABLE_MSAT")
	}
	if cfg.APayInboundMinFinalCltvExpiryDelta != 0 && cfg.APayInboundMinFinalCltvExpiryDelta < ldkHtlcFailBackBuffer+ldkMinFinalCltvBuffer {
		return fmt.Errorf("APAY_INBOUND_MIN_FINAL_CLTV_EXPIRY_DELTA must be >= %d", ldkHtlcFailBackBuffer+ldkMinFinalCltvBuffer)
	}
	if cfg.APayOutboundMinFinalCltvExpiryDelta != 0 && cfg.APayOutboundMinFinalCltvExpiryDelta < ldkHtlcFailBackBuffer+ldkMinFinalCltvBuffer {
		return fmt.Errorf("APAY_OUTBOUND_MIN_FINAL_CLTV_EXPIRY_DELTA must be >= %d", ldkHtlcFailBackBuffer+ldkMinFinalCltvBuffer)
	}
	if err := validateNodeAddress(cfg.LSPNodeHost, cfg.LSPNodePort); err != nil {
		return err
	}
	return nil
}

// Both unset is allowed and omits the fields; a half-configured pair is not,
// since a wrong address fails connectPeer with nothing to diagnose.
func validateNodeAddress(host string, port int) error {
	if host == "" && port == 0 {
		return nil
	}
	if host == "" {
		return errors.New("LSP_NODE_PORT is set without LSP_NODE_HOST")
	}
	if port == 0 {
		return errors.New("LSP_NODE_HOST is set without LSP_NODE_PORT")
	}
	// A bare IPv6 literal is fine here; "example.com:9735" is a misplaced port.
	if strings.Contains(host, "/") || (strings.Contains(host, ":") && net.ParseIP(host) == nil) {
		return fmt.Errorf("LSP_NODE_HOST must be a bare host, not %q — the port goes in LSP_NODE_PORT", host)
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("LSP_NODE_PORT %d is not in 1..65535", port)
	}
	return nil
}

func (cfg Config) claimMarginBlocks() uint32 {
	if cfg.APayClaimMarginBlocks == 0 {
		return defaultAPayClaimMarginBlocks
	}
	return cfg.APayClaimMarginBlocks
}

func envOrDefault(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func intOrDefault(k string, d int) int {
	v := os.Getenv(k)
	if v == "" {
		return d
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return d
	}
	return i
}

func uint16OrDefault(k string, d uint16) uint16 {
	v := os.Getenv(k)
	if v == "" {
		return d
	}
	i, err := strconv.Atoi(v)
	if err != nil || i <= 0 || i > math.MaxUint16 {
		return d
	}
	return uint16(i)
}

func uint32OrDefault(k string, d uint32) uint32 {
	v := os.Getenv(k)
	if v == "" {
		return d
	}
	i, err := strconv.Atoi(v)
	if err != nil || i <= 0 || uint64(i) > math.MaxUint32 {
		return d
	}
	return uint32(i)
}

func durationOrDefault(k string, d time.Duration) time.Duration {
	v := os.Getenv(k)
	if v == "" {
		return d
	}
	dur, err := time.ParseDuration(v)
	if err != nil {
		return d
	}
	return dur
}

func csvOrDefault(k, d string) []string {
	v := os.Getenv(k)
	if v == "" {
		v = d
	}
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func boolOrDefault(k string, d bool) bool {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return d
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return d
	}
}

func validateLightningAddressDomainURL(v string) error {
	_, err := parseLightningAddressDomainURL(v)
	return err
}

func parseLightningAddressDomainURL(v string) (*url.URL, error) {
	if strings.TrimSpace(v) == "" {
		return nil, errors.New("empty URL")
	}

	parsed, err := url.Parse(strings.TrimSpace(v))
	if err != nil {
		return nil, err
	}
	switch parsed.Scheme {
	case "http", "https":
	default:
		return nil, errors.New("must use http or https scheme")
	}
	if parsed.Host == "" {
		return nil, errors.New("missing host")
	}
	if parsed.User != nil {
		return nil, errors.New("userinfo is not allowed")
	}
	if parsed.Path != "" {
		return nil, errors.New("path is not allowed")
	}
	if parsed.RawQuery != "" {
		return nil, errors.New("query is not allowed")
	}
	if parsed.Fragment != "" {
		return nil, errors.New("fragment is not allowed")
	}
	return parsed, nil
}
