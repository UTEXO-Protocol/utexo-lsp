package lspapi

import (
	"errors"
	"fmt"
	"log"
	"math"
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
	defaultAPayOutboundMinFinalCltvExpiryDelta uint16        = 18
	defaultAPayClaimMarginBlocks               uint32        = 12
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

	LightningAddressDomainURL        string
	LightningAddressShortDescription string
	LightningAddressMinSendableMsat  uint64
	LightningAddressMaxSendableMsat  uint64
	APayBearerToken                  string
	APayInboundInvoiceExpiry         time.Duration
	APayOutboundInvoiceExpiry        time.Duration
	APayInboundMinFinalCltvExpiryDelta  uint16
	APayOutboundMinFinalCltvExpiryDelta uint16
	APayClaimMarginBlocks            uint32
	APayRequestOutboundInvoicePath   string

	OpenConnectionPath  string
	GetInfoPath         string
	ListConnectionsPath string
	ListChannelsPath    string
	OpenChannelPath     string
	LNInvoicePath       string
	InvoiceStatusPath   string
	CancelLNInvoicePath string
	SendRGBPath         string
	SendLNPath          string

	DecodeLNPath         string
	DecodeRGBPath        string
	RGBInvoicePath       string
	RefreshTransfersPath string
	ListTransfersPath    string
	ListUnspentsPath     string
	CreateUtxosPath      string
	BlockHeightInfoPath  string

	DefaultChannelCapacitySat uint64
	DefaultChannelAssetAmount uint64
	DefaultChannelPushMsat    uint64
	SupportedAssetIDs         []string
	DefaultVirtualOpenMode    string
	UtxoMinCount              uint32
	UtxoTargetCount           uint32
	UtxoSizeSat               uint32
	UtxoFeeRate               uint64
	UtxoSkipSync              bool
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
		APayRequestOutboundInvoicePath:      envOrDefault("APAY_REQUEST_OUTBOUND_INVOICE_PATH", "/apay/outboundinvoice"),

		OpenConnectionPath:               envOrDefault("LSP_OPENCONNECTION_PATH", "/connectpeer"),
		GetInfoPath:                      envOrDefault("LSP_GET_INFO_PATH", "/nodeinfo"),
		ListConnectionsPath:              envOrDefault("LSP_LISTCONNECTIONS_PATH", "/listpeers"),
		ListChannelsPath:                 envOrDefault("LSP_LISTCHANNELS_PATH", "/listchannels"),
		OpenChannelPath:                  envOrDefault("LSP_OPENCHANNEL_PATH", "/openchannel"),
		LNInvoicePath:                    envOrDefault("LSP_LNINVOICE_PATH", "/lninvoice"),
		InvoiceStatusPath:                envOrDefault("LSP_INVOICESTATUS_PATH", "/invoicestatus"),
		CancelLNInvoicePath:              os.Getenv("LSP_CANCELLNINVOICE_PATH"),
		SendRGBPath:                      envOrDefault("LSP_SENDRGB_PATH", "/sendrgb"),
		SendLNPath:                       envOrDefault("LSP_SENDLN_PATH", "/sendpayment"),
		DecodeLNPath:                     envOrDefault("RGB_DECODE_LN_PATH", "/decodelninvoice"),
		DecodeRGBPath:                    envOrDefault("RGB_DECODE_RGB_PATH", "/decodergbinvoice"),
		RGBInvoicePath:                   envOrDefault("RGB_INVOICE_PATH", "/rgbinvoice"),
		RefreshTransfersPath:             envOrDefault("RGB_REFRESH_TRANSFERS_PATH", "/refreshtransfers"),
		ListTransfersPath:                envOrDefault("RGB_LIST_TRANSFERS_PATH", "/listtransfers"),
		ListUnspentsPath:                 envOrDefault("RGB_LIST_UNSPENTS_PATH", "/listunspents"),
		CreateUtxosPath:                  envOrDefault("RGB_CREATE_UTXOS_PATH", "/createutxos"),
		BlockHeightInfoPath:              envOrDefault("BLOCK_HEIGHT_INFO_PATH", "/networkinfo"),

		DefaultChannelCapacitySat:        uint64(intOrDefault("DEFAULT_CHANNEL_CAPACITY_SAT", 200000)),
		DefaultChannelAssetAmount:        uint64(intOrDefault("DEFAULT_CHANNEL_ASSET_AMOUNT", 1)),
		DefaultChannelPushMsat:           uint64(intOrDefault("DEFAULT_CHANNEL_PUSH_MSAT", 0)),
		SupportedAssetIDs:                csvOrDefault("SUPPORTED_ASSET_IDS", ""),
		DefaultVirtualOpenMode:           strings.TrimSpace(os.Getenv("DEFAULT_VIRTUAL_OPEN_MODE")),
		UtxoMinCount:                     uint32(intOrDefault("UTXO_MIN_COUNT", 0)),
		UtxoTargetCount:                  uint32(intOrDefault("UTXO_TARGET_COUNT", 0)),
		UtxoSizeSat:                      uint32(intOrDefault("UTXO_SIZE_SAT", 32000)),
		UtxoFeeRate:                      uint64(intOrDefault("UTXO_FEE_RATE", 1)),
		UtxoSkipSync:                     boolOrDefault("UTXO_SKIP_SYNC", false),
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
	if cfg.LSPBaseURL == "" {
		log.Fatal("LSP_BASE_URL is required")
	}
	return cfg
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
