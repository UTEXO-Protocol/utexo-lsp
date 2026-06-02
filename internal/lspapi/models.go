package lspapi

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type API struct {
	cfg       Config
	db        Store
	lspClient *NodeClient
	rgbClient *NodeClient
}

type OnchainSendRequest struct {
	RGBInvoice string         `json:"rgb_invoice"`
	LNInvoice  LNInvoiceInput `json:"lninvoice"`
}

type LNInvoiceInput struct {
	AmtMsat                 *uint64 `json:"amt_msat,omitempty"`
	ExpirySec               uint32  `json:"expiry_sec"`
	AssetID                 *string `json:"asset_id,omitempty"`
	AssetAmount             *uint64 `json:"asset_amount,omitempty"`
	DescriptionHash         *string `json:"description_hash,omitempty"`
	PaymentHash             *string `json:"payment_hash,omitempty"`
	MinFinalCltvExpiryDelta *uint16 `json:"min_final_cltv_expiry_delta,omitempty"`
}

type OnchainSendResponse struct {
	RGBInvoice string `json:"rgb_invoice"`
	LNInvoice  string `json:"ln_invoice"`
	MappingID  int64  `json:"mapping_id"`
}

type LightningReceiveRequest struct {
	LNInvoice string          `json:"ln_invoice"`
	RGBParams RGBInvoiceInput `json:"rgb_invoice"`
}

type RGBInvoiceInput struct {
	AssetID          *string `json:"asset_id,omitempty"`
	Assignment       *string `json:"assignment,omitempty"`
	DurationSeconds  *uint32 `json:"duration_seconds,omitempty"`
	MinConfirmations uint8   `json:"min_confirmations"`
	Witness          bool    `json:"witness"`
}

type LightningReceiveResponse struct {
	LNInvoice  string `json:"ln_invoice"`
	RGBInvoice string `json:"rgb_invoice"`
	MappingID  int64  `json:"mapping_id"`
}

type LightningAddressDiscoveryResponse struct {
	Callback    string `json:"callback"`
	MaxSendable uint64 `json:"maxSendable"`
	MinSendable uint64 `json:"minSendable"`
	Metadata    string `json:"metadata"`
	Tag         string `json:"tag"`
}

type LightningAddressCallbackResponse struct {
	PR     string   `json:"pr"`
	Routes []string `json:"routes"`
}

type networkInfoResponse struct {
	Height uint32 `json:"height"`
}

type decodeLNResponse struct {
	AmtMsat                 *uint64 `json:"amt_msat"`
	AssetAmount             *uint64 `json:"asset_amount"`
	AssetID                 *string `json:"asset_id"`
	DescriptionHash         *string `json:"description_hash"`
	ExpirySec               uint64  `json:"expiry_sec"`
	PaymentHash             string  `json:"payment_hash"`
	PayeePubkey             *string `json:"payee_pubkey"`
	MinFinalCltvExpiryDelta uint64  `json:"min_final_cltv_expiry_delta"`
	Timestamp               uint64  `json:"timestamp"`
}

type decodeRGBResponse struct {
	RecipientID         string   `json:"recipient_id"`
	Assignment          any      `json:"assignment"`
	AssetID             *string  `json:"asset_id"`
	ExpirationTimestamp *int64   `json:"expiration_timestamp"`
	TransportEndpoints  []string `json:"transport_endpoints"`
}

type invoiceStatusResponse struct {
	Status string `json:"status"`
}

type rgbInvoiceResponse struct {
	Invoice             string `json:"invoice"`
	ExpirationTimestamp *int64 `json:"expiration_timestamp"`
	BatchTransferIdx    int64  `json:"batch_transfer_idx"`
}

type listConnectionsResponse struct {
	Connections []Connection `json:"connections"`
}

type Connection struct {
	PeerPubkeyAndOptAddr string          `json:"peer_pubkey_and_opt_addr"`
	CapacitySat          uint64          `json:"capacity_sat"`
	PushMsat             uint64          `json:"push_msat"`
	AssetID              *string         `json:"asset_id"`
	AssetAmount          *uint64         `json:"asset_amount,omitempty"`
	Public               bool            `json:"public"`
	WithAnchors          bool            `json:"with_anchors"`
	AssetDecimals        *uint8          `json:"asset_decimals"`
	OpenChannelParams    json.RawMessage `json:"openchannel_params"`
}

type listChannelsResponse struct {
	Channels []Channel `json:"channels"`
}

type listPeersResponse struct {
	Peers []Peer `json:"peers"`
}

type Peer struct {
	Pubkey string `json:"pubkey"`
}

type listTransfersRequest struct {
	AssetID string `json:"asset_id"`
}

type listTransfersResponse struct {
	Transfers []Transfer `json:"transfers"`
}

type listUnspentsRequest struct {
	SkipSync bool `json:"skip_sync"`
}

type listUnspentsResponse struct {
	Unspents []Unspent `json:"unspents"`
}

type Unspent struct {
	Utxo Utxo `json:"utxo"`
}

type Utxo struct {
	Outpoint  string `json:"outpoint"`
	BtcAmount uint64 `json:"btc_amount"`
	Colorable bool   `json:"colorable"`
}

type createUtxosRequest struct {
	UpTo     bool    `json:"up_to"`
	Num      *uint8  `json:"num,omitempty"`
	Size     *uint32 `json:"size,omitempty"`
	FeeRate  uint64  `json:"fee_rate"`
	SkipSync bool    `json:"skip_sync"`
}

type Transfer struct {
	Idx    int64  `json:"idx"`
	Status string `json:"status"`
}

type Channel struct {
	PeerPubkey string  `json:"peer_pubkey"`
	AssetID    *string `json:"asset_id"`
}

type OpenChannelRequest struct {
	PeerPubkeyAndOptAddr      string  `json:"peer_pubkey_and_opt_addr"`
	CapacitySat               uint64  `json:"capacity_sat"`
	PushMsat                  uint64  `json:"push_msat"`
	AssetID                   *string `json:"asset_id,omitempty"`
	AssetAmount               *uint64 `json:"asset_amount,omitempty"`
	PushAssetAmount           *uint64 `json:"push_asset_amount,omitempty"`
	Public                    bool    `json:"public"`
	WithAnchors               bool    `json:"with_anchors"`
	FeeBaseMsat               *uint32 `json:"fee_base_msat,omitempty"`
	FeeProportionalMillionths *uint32 `json:"fee_proportional_millionths,omitempty"`
	TemporaryChannelID        *string `json:"temporary_channel_id,omitempty"`
	VirtualOpenMode           *string `json:"virtual_open_mode,omitempty"`
}

type OnchainSendRecord struct {
	ID             int64
	UserRGBInvoice string
	LspLNInvoice   string
	Status         string
	LNExpiresAt    *time.Time
	CreatedAt      time.Time
}

type LightningReceiveRecord struct {
	ID               int64
	UserLNInvoice    string
	LspRGBInvoice    string
	RGBAssetID       string
	BatchTransferIdx int64
	Status           string
	RGBExpiresAt     *time.Time
	CreatedAt        time.Time
}

type LightningAddressAccount struct {
	PeerPubkey string
	Username   string
	CreatedAt  time.Time
}

type AsyncInvoiceStatus string

const (
	asyncInvoiceStatusReserved          AsyncInvoiceStatus = "reserved"
	asyncInvoiceStatusActive            AsyncInvoiceStatus = "active"
	asyncInvoiceStatusClaimable         AsyncInvoiceStatus = "claimable"
	asyncInvoiceStatusOutboundRequested AsyncInvoiceStatus = "outbound_requested"
	asyncInvoiceStatusOutboundPending   AsyncInvoiceStatus = "outbound_pending"
	asyncInvoiceStatusOutboundPaid      AsyncInvoiceStatus = "outbound_paid"
	asyncInvoiceStatusOutboundClaimed   AsyncInvoiceStatus = "outbound_claimed"
	asyncInvoiceStatusInboundClaimed    AsyncInvoiceStatus = "inbound_claimed"
	asyncInvoiceStatusInboundCancelled  AsyncInvoiceStatus = "inbound_cancelled"
	asyncInvoiceStatusOutboundCancelled AsyncInvoiceStatus = "outbound_cancelled"
	asyncInvoiceStatusFailed            AsyncInvoiceStatus = "failed"
)

type AsyncOrderStatus string

const (
	asyncOrderStatusActive    AsyncOrderStatus = "active"
	asyncOrderStatusExhausted AsyncOrderStatus = "exhausted"
)

type AsyncOutboxAction string

const (
	asyncOutboxActionRequestOutboundInvoice AsyncOutboxAction = "request_outbound_invoice"
	asyncOutboxActionSendOutboundPayment    AsyncOutboxAction = "send_outbound_payment"
	asyncOutboxActionClaimInboundInvoice    AsyncOutboxAction = "claim_inbound_invoice"
)

type AsyncOutboxStatus string

const (
	asyncOutboxStatusPending    AsyncOutboxStatus = "pending"
	asyncOutboxStatusProcessing AsyncOutboxStatus = "processing"
	asyncOutboxStatusDone       AsyncOutboxStatus = "done"
	asyncOutboxStatusFailed     AsyncOutboxStatus = "failed"
)

type AsyncPoolStatus string

const (
	asyncPoolStatusAvailable AsyncPoolStatus = "available"
	asyncPoolStatusReserved  AsyncPoolStatus = "reserved"
	asyncPoolStatusConsumed  AsyncPoolStatus = "consumed"
)

func scanEnumText(src any) (string, error) {
	switch v := src.(type) {
	case nil:
		return "", nil
	case string:
		return strings.TrimSpace(v), nil
	case []byte:
		return strings.TrimSpace(string(v)), nil
	default:
		return "", fmt.Errorf("cannot scan %T into string enum", src)
	}
}

func (s *AsyncInvoiceStatus) Scan(src any) error {
	v, err := scanEnumText(src)
	if err != nil {
		return err
	}
	*s = AsyncInvoiceStatus(v)
	return nil
}

func (s AsyncInvoiceStatus) Value() (driver.Value, error) {
	return string(s), nil
}

func (s *AsyncOrderStatus) Scan(src any) error {
	v, err := scanEnumText(src)
	if err != nil {
		return err
	}
	*s = AsyncOrderStatus(v)
	return nil
}

func (s AsyncOrderStatus) Value() (driver.Value, error) {
	return string(s), nil
}

func (s *AsyncOutboxAction) Scan(src any) error {
	v, err := scanEnumText(src)
	if err != nil {
		return err
	}
	*s = AsyncOutboxAction(v)
	return nil
}

func (s AsyncOutboxAction) Value() (driver.Value, error) {
	return string(s), nil
}

func (s *AsyncOutboxStatus) Scan(src any) error {
	v, err := scanEnumText(src)
	if err != nil {
		return err
	}
	*s = AsyncOutboxStatus(v)
	return nil
}

func (s AsyncOutboxStatus) Value() (driver.Value, error) {
	return string(s), nil
}

func (s *AsyncPoolStatus) Scan(src any) error {
	v, err := scanEnumText(src)
	if err != nil {
		return err
	}
	*s = AsyncPoolStatus(v)
	return nil
}

func (s AsyncPoolStatus) Value() (driver.Value, error) {
	return string(s), nil
}

type AsyncRotatingInvoice struct {
	ID                  int64
	OrderID             int64
	InvoiceSlot         int64
	HashIndex           int64
	PaymentHash         string
	InboundInvoice      *string
	AssetAmount         *uint64
	AssetID             *string
	AmountMsat          uint64
	ExpiresAt           time.Time
	Status              AsyncInvoiceStatus
	CreatedAt           time.Time
	UpdatedAt           time.Time
	ClaimDeadlineHeight *uint32
	OutboundPendingAt   *time.Time
	OutboundPaidAt      *time.Time
	PaymentPreimage     *string
	RequestInvoiceAt    *time.Time
	OutboundInvoice     *string
}

type AsyncRotatingInvoiceOutboxJob struct {
	ID          int64
	PaymentHash string
	Action      AsyncOutboxAction
	Status      AsyncOutboxStatus
	Attempts    int64
	AvailableAt time.Time
	LockedUntil *time.Time
	LastError   *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type AsyncOrderClaimableRequest struct {
	AmountMsat          uint64  `json:"amount_msat"`
	ClaimDeadlineHeight *uint32 `json:"claim_deadline_height,omitempty"`
	PaymentHash         string  `json:"payment_hash"`
}

type AsyncOrderPaymentSentRequest struct {
	PaymentHash     string `json:"payment_hash"`
	PaymentPreimage string `json:"payment_preimage"`
}

type AsyncOrderRequestOutboundInvoiceParams struct {
	AmountMsat              uint64  `json:"amount_msat"`
	AssetAmount             *uint64 `json:"asset_amount,omitempty"`
	AssetID                 *string `json:"asset_id,omitempty"`
	DescriptionHash         string  `json:"description_hash"`
	InvoiceExpirySec        uint32  `json:"invoice_expiry_sec"`
	MinFinalCltvExpiryDelta uint16  `json:"min_final_cltv_expiry_delta"`
	HashIndex               string  `json:"hash_index"`
	PaymentHash             string  `json:"payment_hash"`
}

type AsyncOrderOutboundInvoiceRequest struct {
	ClientNodeID string                                 `json:"client_node_id"`
	Params       AsyncOrderRequestOutboundInvoiceParams `json:"params"`
}

type AsyncOrderOutboundInvoiceResponse struct {
	Bolt11      string `json:"bolt11"`
	PaymentHash string `json:"payment_hash"`
}

type AsyncOrderNewHashInput struct {
	HashIndex   string `json:"hash_index"`
	PaymentHash string `json:"payment_hash"`
}

// UnmarshalJSON accepts hash_index as either a JSON string ("1") or number (1).
// rgb-lightning-node serializes the LSP HTTP request with numeric hash_index.
func (h *AsyncOrderNewHashInput) UnmarshalJSON(data []byte) error {
	var raw struct {
		HashIndex   json.RawMessage `json:"hash_index"`
		PaymentHash string          `json:"payment_hash"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	h.PaymentHash = raw.PaymentHash

	if len(raw.HashIndex) == 0 {
		return errors.New("hash_index is required")
	}

	var asString string
	if err := json.Unmarshal(raw.HashIndex, &asString); err == nil {
		h.HashIndex = asString
		return nil
	}

	var asNumber json.Number
	if err := json.Unmarshal(raw.HashIndex, &asNumber); err == nil {
		index, err := strconv.ParseInt(asNumber.String(), 10, 64)
		if err != nil || index <= 0 {
			return errors.New("hash_index must be a positive integer")
		}
		h.HashIndex = asNumber.String()
		return nil
	}

	return errors.New("hash_index must be a JSON string or number")
}

type AsyncOrderNewRequest struct {
	ID              any                      `json:"id,omitempty"`
	PeerPubkey      string                   `json:"peer_pubkey"`
	ProtocolVersion uint64                   `json:"protocol_version"`
	Hashes          []AsyncOrderNewHashInput `json:"hashes"`
}

type AsyncOrderNewResponse struct {
	ProtocolVersion      uint64           `json:"protocol_version"`
	OrderID              string           `json:"order_id"`
	Status               AsyncOrderStatus `json:"status"`
	AcceptedThroughIndex uint64           `json:"accepted_through_index"`
	NextIndexExpected    uint64           `json:"next_index_expected"`
	UnusedHashes         uint64           `json:"unused_hashes"`
	RefillBatchSize      uint64           `json:"refill_batch_size"`
}

type AsyncOrderJSONRPCResponseEnvelope struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      any              `json:"id"`
	Result  any              `json:"result,omitempty"`
	Error   *AsyncOrderError `json:"error,omitempty"`
}

type AsyncOrderError struct {
	Code    int64  `json:"code"`
	Message string `json:"message"`
}

func (e AsyncOrderError) Error() string {
	return e.Message
}
