package node_client

import (
	"context"
	"encoding/json"
)

// Assignment represents an assignment in RGB transfer.
type Assignment struct {
	Type  string `json:"type"`  // "Fungible", "NonFungible", etc.
	Value int64  `json:"value"` // For fungible assets - no omitempty to ensure 0 is sent!
}

// NewFungibleAssignment creates a new fungible assignment.
func NewFungibleAssignment(amount int64) Assignment {
	return Assignment{
		Type:  "Fungible",
		Value: amount,
	}
}

// WitnessData represents witness data for RGB transfer.
type WitnessData struct {
	AmountSat int64 `json:"amount_sat"`
	Blinding  int64 `json:"blinding,omitempty"`
}

// AssetBalanceRequest represents the request for /assetbalance endpoint.
type AssetBalanceRequest struct {
	AssetID string `json:"asset_id"`
}

// AssetBalanceResponse represents the response from /assetbalance endpoint.
type AssetBalanceResponse struct {
	Settled          int64 `json:"settled"`
	Future           int64 `json:"future"`
	Spendable        int64 `json:"spendable"`
	OffchainOutbound int64 `json:"offchain_outbound"`
	OffchainInbound  int64 `json:"offchain_inbound"`
}

// AssetBalance calls the /assetbalance endpoint.
func (c *Client) AssetBalance(ctx context.Context, req AssetBalanceRequest) (AssetBalanceResponse, error) {
	var resp AssetBalanceResponse
	if err := c.post(ctx, "/assetbalance", req, &resp); err != nil {
		return AssetBalanceResponse{}, err
	}
	return resp, nil
}

// AssetMetadataRequest represents the request for /assetmetadata endpoint.
type AssetMetadataRequest struct {
	AssetID string `json:"asset_id"`
}

// AssetMetadataResponse represents the response from /assetmetadata endpoint.
type AssetMetadataResponse struct {
	AssetSchema            string  `json:"asset_schema"` // "Nia", "Uda", "Cfa"
	InitialSupply          int64   `json:"initial_supply,omitempty"`
	MaxSupply              int64   `json:"max_supply,omitempty"`
	KnownCirculatingSupply int64   `json:"known_circulating_supply,omitempty"`
	IssuedSupply           int64   `json:"issued_supply,omitempty"`
	Timestamp              int64   `json:"timestamp"`
	Name                   string  `json:"name"`
	Precision              int     `json:"precision"`
	Ticker                 string  `json:"ticker,omitempty"`
	Details                *string `json:"details,omitempty"`
}

// AssetMetadata calls the /assetmetadata endpoint.
func (c *Client) AssetMetadata(ctx context.Context, req AssetMetadataRequest) (AssetMetadataResponse, error) {
	var resp AssetMetadataResponse
	if err := c.post(ctx, "/assetmetadata", req, &resp); err != nil {
		return AssetMetadataResponse{}, err
	}
	return resp, nil
}

// ListAssetsRequest represents the request for /listassets endpoint.
type ListAssetsRequest struct {
	FilterAssetSchemas []string `json:"filter_asset_schemas"`
}

// Media represents media information for an asset.
type Media struct {
	FilePath string `json:"file_path,omitempty"`
	Mime     string `json:"mime,omitempty"`
}

// AssetNIA represents an NIA (fungible) asset.
type AssetNIA struct {
	AssetID      string                `json:"asset_id"`
	Ticker       string                `json:"ticker"`
	Name         string                `json:"name"`
	Details      string                `json:"details,omitempty"`
	Precision    int                   `json:"precision"`
	IssuedSupply int64                 `json:"issued_supply"`
	Timestamp    int64                 `json:"timestamp"`
	AddedAt      int64                 `json:"added_at"`
	Balance      *AssetBalanceResponse `json:"balance,omitempty"`
	Media        *Media                `json:"media,omitempty"`
}

// AssetUDA represents a UDA (unique) asset.
type AssetUDA struct {
	AssetID   string                `json:"asset_id"`
	Ticker    string                `json:"ticker"`
	Name      string                `json:"name"`
	Details   string                `json:"details,omitempty"`
	Precision int                   `json:"precision"`
	Timestamp int64                 `json:"timestamp"`
	AddedAt   int64                 `json:"added_at"`
	Balance   *AssetBalanceResponse `json:"balance,omitempty"`
}

// AssetCFA represents a CFA (collectible) asset.
type AssetCFA struct {
	AssetID      string                `json:"asset_id"`
	Name         string                `json:"name"`
	Details      string                `json:"details,omitempty"`
	Precision    int                   `json:"precision"`
	IssuedSupply int64                 `json:"issued_supply"`
	Timestamp    int64                 `json:"timestamp"`
	AddedAt      int64                 `json:"added_at"`
	Balance      *AssetBalanceResponse `json:"balance,omitempty"`
	Media        *Media                `json:"media,omitempty"`
}

// ListAssetsResponse represents the response from /listassets endpoint.
type ListAssetsResponse struct {
	NIA []AssetNIA `json:"nia,omitempty"`
	UDA []AssetUDA `json:"uda,omitempty"`
	CFA []AssetCFA `json:"cfa,omitempty"`
}

// ListAssets calls the /listassets endpoint.
func (c *Client) ListAssets(ctx context.Context, req ListAssetsRequest) (ListAssetsResponse, error) {
	if req.FilterAssetSchemas == nil {
		req.FilterAssetSchemas = []string{}
	}
	var resp ListAssetsResponse
	if err := c.post(ctx, "/listassets", req, &resp); err != nil {
		return ListAssetsResponse{}, err
	}
	return resp, nil
}

// EstimateFeeRequest represents the request for /estimatefee endpoint.
type EstimateFeeRequest struct {
	Blocks int `json:"blocks"`
}

// EstimateFeeResponse represents the response from /estimatefee endpoint.
type EstimateFeeResponse struct {
	FeeRate float64 `json:"fee_rate"`
}

// EstimateFee calls the /estimatefee endpoint.
func (c *Client) EstimateFee(ctx context.Context, req EstimateFeeRequest) (EstimateFeeResponse, error) {
	var resp EstimateFeeResponse
	if err := c.post(ctx, "/estimatefee", req, &resp); err != nil {
		return EstimateFeeResponse{}, err
	}
	return resp, nil
}

// LNInvoiceRequest represents the request for /lninvoice endpoint.
type LNInvoiceRequest struct {
	AmtMsat                 *uint64 `json:"amt_msat,omitempty"`
	ExpirySec               uint32  `json:"expiry_sec"`
	AssetID                 *string `json:"asset_id,omitempty"`
	AssetAmount             *uint64 `json:"asset_amount,omitempty"`
	DescriptionHash         *string `json:"description_hash,omitempty"`
	PaymentHash             *string `json:"payment_hash,omitempty"`
	MinFinalCltvExpiryDelta *uint16 `json:"min_final_cltv_expiry_delta,omitempty"`
}

// LNInvoiceResponse represents the response from /lninvoice endpoint.
type LNInvoiceResponse struct {
	Invoice string `json:"invoice"`
}

// LNInvoice calls the /lninvoice endpoint to create a Lightning invoice.
func (c *Client) LNInvoice(ctx context.Context, req LNInvoiceRequest) (LNInvoiceResponse, error) {
	var resp LNInvoiceResponse
	if err := c.post(ctx, "/lninvoice", req, &resp); err != nil {
		return LNInvoiceResponse{}, err
	}
	return resp, nil
}

// HodlInvoiceRequest represents the request for /invoice/hodl endpoint.
type HodlInvoiceRequest struct {
	AmtMsat     int64  `json:"amt_msat,omitempty"`
	ExpirySec   int64  `json:"expiry_sec"`
	AssetID     string `json:"asset_id,omitempty"`
	AssetAmount int64  `json:"asset_amount,omitempty"`
	PaymentHash string `json:"payment_hash"`
	ExternalRef string `json:"external_ref,omitempty"`
}

// HodlInvoiceResponse represents the response from /invoice/hodl endpoint.
type HodlInvoiceResponse struct {
	Invoice       string `json:"invoice"`
	PaymentSecret string `json:"payment_secret"`
}

// HodlInvoice calls the /invoice/hodl endpoint to create a HODL invoice.
func (c *Client) HodlInvoice(ctx context.Context, req HodlInvoiceRequest) (HodlInvoiceResponse, error) {
	var resp HodlInvoiceResponse
	if err := c.post(ctx, "/invoice/hodl", req, &resp); err != nil {
		return HodlInvoiceResponse{}, err
	}
	return resp, nil
}

// SettleInvoiceRequest represents the request for /invoice/settle endpoint.
type SettleInvoiceRequest struct {
	PaymentHash     string `json:"payment_hash"`
	PaymentPreimage string `json:"payment_preimage"`
}

// SettleInvoice calls the /invoice/settle endpoint to settle a HODL invoice.
func (c *Client) SettleInvoice(ctx context.Context, req SettleInvoiceRequest) error {
	var resp struct{}
	return c.post(ctx, "/invoice/settle", req, &resp)
}

// CancelInvoiceRequest represents the request for /invoice/cancel endpoint.
type CancelInvoiceRequest struct {
	PaymentHash string `json:"payment_hash"`
}

// CancelInvoice calls the /invoice/cancel endpoint to cancel a HODL invoice.
func (c *Client) CancelInvoice(ctx context.Context, req CancelInvoiceRequest) error {
	var resp struct{}
	return c.post(ctx, "/invoice/cancel", req, &resp)
}

// DecodeLNInvoiceRequest represents the request for /decodelninvoice endpoint.
type DecodeLNInvoiceRequest struct {
	Invoice string `json:"invoice"`
}

// DecodeLNInvoiceResponse represents the response from /decodelninvoice endpoint.
type DecodeLNInvoiceResponse struct {
	AmtMsat                 int64  `json:"amt_msat,omitempty"`
	ExpirySec               int64  `json:"expiry_sec"`
	Timestamp               int64  `json:"timestamp"`
	AssetID                 string `json:"asset_id,omitempty"`
	AssetAmount             int64  `json:"asset_amount,omitempty"`
	DescriptionHash         string `json:"description_hash,omitempty"`
	PaymentHash             string `json:"payment_hash"`
	PaymentSecret           string `json:"payment_secret"`
	PayeePubkey             string `json:"payee_pubkey"`
	MinFinalCltvExpiryDelta uint64 `json:"min_final_cltv_expiry_delta,omitempty"`
	Network                 string `json:"network"` // "Mainnet", "Testnet", "Signet", "Regtest"
}

// DecodeLNInvoice calls the /decodelninvoice endpoint.
func (c *Client) DecodeLNInvoice(ctx context.Context, req DecodeLNInvoiceRequest) (DecodeLNInvoiceResponse, error) {
	var resp DecodeLNInvoiceResponse
	if err := c.post(ctx, "/decodelninvoice", req, &resp); err != nil {
		return DecodeLNInvoiceResponse{}, err
	}
	return resp, nil
}

// SendPaymentRequest represents the request for /sendpayment endpoint.
type SendPaymentRequest struct {
	Invoice string `json:"invoice"`
}

// SendPaymentResponse represents the response from /sendpayment endpoint.
type SendPaymentResponse struct {
	PaymentHash   string `json:"payment_hash"`
	PaymentSecret string `json:"payment_secret"`
	Status        string `json:"status"` // "Pending", "Succeeded", "Failed"
}

// SendPayment calls the /sendpayment endpoint to pay a Lightning invoice.
func (c *Client) SendPayment(ctx context.Context, req SendPaymentRequest) (SendPaymentResponse, error) {
	var resp SendPaymentResponse
	if err := c.post(ctx, "/sendpayment", req, &resp); err != nil {
		return SendPaymentResponse{}, err
	}
	return resp, nil
}

// KeysendRequest represents the request for /keysend endpoint.
type KeysendRequest struct {
	DestPubkey  string `json:"dest_pubkey"`
	AmtMsat     int64  `json:"amt_msat"`
	AssetID     string `json:"asset_id,omitempty"`
	AssetAmount int64  `json:"asset_amount,omitempty"`
}

// KeysendResponse represents the response from /keysend endpoint.
type KeysendResponse struct {
	PaymentHash     string `json:"payment_hash"`
	PaymentPreimage string `json:"payment_preimage"`
	Status          string `json:"status"` // "Pending", "Succeeded", "Failed"
}

// Keysend calls the /keysend endpoint for spontaneous payments.
func (c *Client) Keysend(ctx context.Context, req KeysendRequest) (KeysendResponse, error) {
	var resp KeysendResponse
	if err := c.post(ctx, "/keysend", req, &resp); err != nil {
		return KeysendResponse{}, err
	}
	return resp, nil
}

// Payment represents a Lightning payment.
type Payment struct {
	AmtMsat     int64  `json:"amt_msat"`
	AssetAmount int64  `json:"asset_amount,omitempty"`
	AssetID     string `json:"asset_id,omitempty"`
	PaymentHash string `json:"payment_hash"`
	Inbound     bool   `json:"inbound"`
	Status      string `json:"status"` // "Pending", "Succeeded", "Failed"
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
	PayeePubkey string `json:"payee_pubkey,omitempty"`
}

// ListPaymentsResponse represents the response from /listpayments endpoint.
type ListPaymentsResponse struct {
	Payments []Payment `json:"payments"`
}

// ListPayments calls the /listpayments endpoint (GET).
func (c *Client) ListPayments(ctx context.Context) (ListPaymentsResponse, error) {
	var resp ListPaymentsResponse
	if err := c.get(ctx, "/listpayments", &resp); err != nil {
		return ListPaymentsResponse{}, err
	}
	return resp, nil
}

// GetPaymentRequest represents the request for /getpayment endpoint.
type GetPaymentRequest struct {
	PaymentHash string `json:"payment_hash"`
	PaymentType string `json:"payment_type"`
}

// GetPaymentResponse represents the response from /getpayment endpoint.
type GetPaymentResponse struct {
	Payment *Payment `json:"payment"`
}

// GetPayment calls the /getpayment endpoint.
func (c *Client) GetPayment(ctx context.Context, req GetPaymentRequest) (GetPaymentResponse, error) {
	var resp GetPaymentResponse
	if err := c.post(ctx, "/getpayment", req, &resp); err != nil {
		return GetPaymentResponse{}, err
	}
	return resp, nil
}

// InvoiceStatusRequest represents the request for /invoicestatus endpoint.
type InvoiceStatusRequest struct {
	Invoice string `json:"invoice"`
}

// InvoiceStatusResponse represents the response from /invoicestatus endpoint.
type InvoiceStatusResponse struct {
	Status string `json:"status"` // "Pending", "Succeeded", "Cancelled", "Failed", "Expired"
}

// InvoiceStatus calls the /invoicestatus endpoint.
func (c *Client) InvoiceStatus(ctx context.Context, req InvoiceStatusRequest) (InvoiceStatusResponse, error) {
	var resp InvoiceStatusResponse
	if err := c.post(ctx, "/invoicestatus", req, &resp); err != nil {
		return InvoiceStatusResponse{}, err
	}
	return resp, nil
}

// ClaimHodlInvoiceRequest represents the request for /claimhodlinvoice endpoint.
type ClaimHodlInvoiceRequest struct {
	PaymentHash     string `json:"payment_hash"`
	PaymentPreimage string `json:"payment_preimage"`
}

// ClaimHodlInvoiceResponse represents the response from /claimhodlinvoice endpoint.
type ClaimHodlInvoiceResponse struct {
	Success bool `json:"success,omitempty"`
}

// ClaimHodlInvoice calls the /claimhodlinvoice endpoint.
func (c *Client) ClaimHodlInvoice(ctx context.Context, req ClaimHodlInvoiceRequest) (ClaimHodlInvoiceResponse, error) {
	var resp ClaimHodlInvoiceResponse
	if err := c.post(ctx, "/claimhodlinvoice", req, &resp); err != nil {
		return ClaimHodlInvoiceResponse{}, err
	}
	return resp, nil
}

// NodeInfoResponse represents the response from /nodeinfo endpoint.
type NodeInfoResponse struct {
	Pubkey                  string `json:"pubkey"`
	NumChannels             int    `json:"num_channels"`
	NumUsableChannels       int    `json:"num_usable_channels"`
	LocalBalanceSat         int64  `json:"local_balance_sat"`
	EventualCloseFeesSat    int64  `json:"eventual_close_fees_sat"`
	PendingOutboundPayments int64  `json:"pending_outbound_payments_sat"`
	NumPeers                int    `json:"num_peers"`
	AccountXpubVanilla      string `json:"account_xpub_vanilla"`
	AccountXpubColored      string `json:"account_xpub_colored"`
	MaxMediaUploadSizeMB    int    `json:"max_media_upload_size_mb"`
	RgbHtlcMinMsat          int64  `json:"rgb_htlc_min_msat"`
	RgbChannelCapacityMin   int64  `json:"rgb_channel_capacity_min_sat"`
	ChannelCapacityMinSat   int64  `json:"channel_capacity_min_sat"`
	ChannelCapacityMaxSat   int64  `json:"channel_capacity_max_sat"`
	ChannelAssetMinAmount   int64  `json:"channel_asset_min_amount"`
	ChannelAssetMaxAmount   uint64 `json:"channel_asset_max_amount"`
	NetworkNodes            int    `json:"network_nodes"`
	NetworkChannels         int    `json:"network_channels"`
}

// NodeInfo calls the /nodeinfo endpoint (GET).
func (c *Client) NodeInfo(ctx context.Context) (NodeInfoResponse, error) {
	var resp NodeInfoResponse
	if err := c.get(ctx, "/nodeinfo", &resp); err != nil {
		return NodeInfoResponse{}, err
	}
	return resp, nil
}

// AsyncOrderRequestOutboundInvoiceParams represents the outbound invoice params for /apay/outboundinvoice.
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

// AsyncOrderOutboundInvoiceRequest represents the request for /apay/outboundinvoice.
type AsyncOrderOutboundInvoiceRequest struct {
	ClientNodeID string                                 `json:"client_node_id"`
	Params       AsyncOrderRequestOutboundInvoiceParams `json:"params"`
}

// AsyncOrderOutboundInvoiceResponse represents the response from /apay/outboundinvoice.
type AsyncOrderOutboundInvoiceResponse struct {
	Bolt11      string `json:"bolt11"`
	PaymentHash string `json:"payment_hash"`
}

// AsyncOrderOutboundInvoice calls the /apay/outboundinvoice endpoint.
func (c *Client) AsyncOrderOutboundInvoice(ctx context.Context, req AsyncOrderOutboundInvoiceRequest) (AsyncOrderOutboundInvoiceResponse, error) {
	var resp AsyncOrderOutboundInvoiceResponse
	if err := c.post(ctx, "/apay/outboundinvoice", req, &resp); err != nil {
		return AsyncOrderOutboundInvoiceResponse{}, err
	}
	return resp, nil
}

// NetworkInfoResponse represents the response from /networkinfo endpoint.
type NetworkInfoResponse struct {
	Network string `json:"network"` // "Mainnet", "Testnet", "Signet", "Regtest"
	Height  int64  `json:"height"`
}

// NetworkInfo calls the /networkinfo endpoint (GET).
func (c *Client) NetworkInfo(ctx context.Context) (NetworkInfoResponse, error) {
	var resp NetworkInfoResponse
	if err := c.get(ctx, "/networkinfo", &resp); err != nil {
		return NetworkInfoResponse{}, err
	}
	return resp, nil
}

// Peer represents a Lightning peer.
type Peer struct {
	Pubkey string `json:"pubkey"`
}

// ListPeersResponse represents the response from /listpeers endpoint.
type ListPeersResponse struct {
	Peers []Peer `json:"peers"`
}

// Connection represents a desired Lightning channel connection.
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

// ListConnectionsResponse represents the response from /listconnections endpoint.
type ListConnectionsResponse struct {
	Connections []Connection `json:"connections"`
}

// ListPeers calls the /listpeers endpoint (GET).
func (c *Client) ListPeers(ctx context.Context) (ListPeersResponse, error) {
	var resp ListPeersResponse
	if err := c.get(ctx, "/listpeers", &resp); err != nil {
		return ListPeersResponse{}, err
	}
	return resp, nil
}

// ListConnections calls the /listconnections endpoint (GET).
func (c *Client) ListConnections(ctx context.Context) (ListConnectionsResponse, error) {
	var resp ListConnectionsResponse
	if err := c.get(ctx, "/listconnections", &resp); err != nil {
		return ListConnectionsResponse{}, err
	}
	return resp, nil
}

// Channel represents a Lightning channel.
type Channel struct {
	PeerPubkey string  `json:"peer_pubkey"`
	AssetID    *string `json:"asset_id,omitempty"`
}

// ListChannelsResponse represents the response from /listchannels endpoint.
type ListChannelsResponse struct {
	Channels []Channel `json:"channels"`
}

// ListChannels calls the /listchannels endpoint (GET).
func (c *Client) ListChannels(ctx context.Context) (ListChannelsResponse, error) {
	var resp ListChannelsResponse
	if err := c.get(ctx, "/listchannels", &resp); err != nil {
		return ListChannelsResponse{}, err
	}
	return resp, nil
}

// OpenChannelRequest represents the request for /openchannel endpoint.
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

// OpenChannelResponse represents the response from /openchannel endpoint.
type OpenChannelResponse struct {
	TemporaryChannelID string `json:"temporary_channel_id,omitempty"`
}

// OpenChannel calls the /openchannel endpoint.
func (c *Client) OpenChannel(ctx context.Context, req OpenChannelRequest) (OpenChannelResponse, error) {
	var resp OpenChannelResponse
	if err := c.post(ctx, "/openchannel", req, &resp); err != nil {
		return OpenChannelResponse{}, err
	}
	return resp, nil
}

// DecodeRGBInvoiceRequest represents the request for /decodergbinvoice endpoint.
type DecodeRGBInvoiceRequest struct {
	Invoice string `json:"invoice"`
}

// DecodeRGBInvoiceResponse represents the response from /decodergbinvoice endpoint.
type DecodeRGBInvoiceResponse struct {
	RecipientID         string   `json:"recipient_id"`
	Assignment          any      `json:"assignment"`
	AssetID             *string  `json:"asset_id,omitempty"`
	ExpirationTimestamp *int64   `json:"expiration_timestamp,omitempty"`
	TransportEndpoints  []string `json:"transport_endpoints,omitempty"`
}

// DecodeRGBInvoice calls the /decodergbinvoice endpoint.
func (c *Client) DecodeRGBInvoice(ctx context.Context, req DecodeRGBInvoiceRequest) (DecodeRGBInvoiceResponse, error) {
	var resp DecodeRGBInvoiceResponse
	if err := c.post(ctx, "/decodergbinvoice", req, &resp); err != nil {
		return DecodeRGBInvoiceResponse{}, err
	}
	return resp, nil
}

// RGBInvoiceRequest represents the request for /rgbinvoice endpoint.
type RGBInvoiceRequest struct {
	AssetID             *string `json:"asset_id,omitempty"`
	Assignment          any     `json:"assignment"`
	ExpirationTimestamp *int64  `json:"expiration_timestamp,omitempty"`
	MinConfirmations    uint8   `json:"min_confirmations"`
	Witness             bool    `json:"witness"`
}

// RGBInvoiceResponse represents the response from /rgbinvoice endpoint.
type RGBInvoiceResponse struct {
	Invoice             string `json:"invoice"`
	ExpirationTimestamp *int64 `json:"expiration_timestamp,omitempty"`
	BatchTransferIdx    int64  `json:"batch_transfer_idx"`
}

// RGBInvoice calls the /rgbinvoice endpoint.
func (c *Client) RGBInvoice(ctx context.Context, req RGBInvoiceRequest) (RGBInvoiceResponse, error) {
	var resp RGBInvoiceResponse
	if err := c.post(ctx, "/rgbinvoice", req, &resp); err != nil {
		return RGBInvoiceResponse{}, err
	}
	return resp, nil
}

// RefreshTransfersRequest represents the request for /refreshtransfers endpoint.
type RefreshTransfersRequest struct {
	SkipSync bool  `json:"skip_sync"`
	Filter   []any `json:"filter"`
}

// RefreshTransfers calls the /refreshtransfers endpoint.
func (c *Client) RefreshTransfers(ctx context.Context, req RefreshTransfersRequest) error {
	if req.Filter == nil {
		req.Filter = []any{}
	}
	var resp struct{}
	return c.post(ctx, "/refreshtransfers", req, &resp)
}

// ListTransfersRequest represents the request for /listtransfers endpoint.
type ListTransfersRequest struct {
	AssetID string `json:"asset_id"`
}

type TransferStatus string

const (
	TransferStatusInitiated            TransferStatus = "Initiated"
	TransferStatusWaitingCounterparty  TransferStatus = "WaitingCounterparty"
	TransferStatusWaitingConfirmations TransferStatus = "WaitingConfirmations"
	TransferStatusSettled              TransferStatus = "Settled"
	TransferStatusFailed               TransferStatus = "Failed"
)

type TransferKind string

const (
	TransferKindIssuance       TransferKind = "Issuance"
	TransferKindReceiveBlind   TransferKind = "ReceiveBlind"
	TransferKindReceiveWitness TransferKind = "ReceiveWitness"
	TransferKindSend           TransferKind = "Send"
	TransferKindInflation      TransferKind = "Inflation"
)

// Transfer represents an RGB transfer.
type Transfer struct {
	Idx    int64          `json:"idx"`
	Status TransferStatus `json:"status"`
	Kind   TransferKind   `json:"kind"`
}

// #[derive(Debug, PartialEq, Deserialize, Serialize)]
// pub(crate) enum TransferStatus {
//     Initiated,
//     WaitingCounterparty,
//     WaitingConfirmations,
//     Settled,
//     Failed,
// }
//
// #[derive(Debug, PartialEq, Deserialize, Serialize)]
// pub(crate) enum TransferKind {
//     Issuance,
//     ReceiveBlind,
//     ReceiveWitness,
//     Send,
//     Inflation,
// }

// ListTransfersResponse represents the response from /listtransfers endpoint.
type ListTransfersResponse struct {
	Transfers []Transfer `json:"transfers"`
}

// ListTransfers calls the /listtransfers endpoint.
func (c *Client) ListTransfers(ctx context.Context, req ListTransfersRequest) (ListTransfersResponse, error) {
	var resp ListTransfersResponse
	if err := c.post(ctx, "/listtransfers", req, &resp); err != nil {
		return ListTransfersResponse{}, err
	}
	return resp, nil
}

// SendRGBRecipient represents a single RGB send recipient.
type SendRGBRecipient struct {
	RecipientID        string   `json:"recipient_id"`
	Assignment         any      `json:"assignment"`
	TransportEndpoints []string `json:"transport_endpoints"`
}

// SendRGBRequest represents the request for /sendrgb endpoint.
type SendRGBRequest struct {
	Donation         bool                          `json:"donation"`
	FeeRate          uint64                        `json:"fee_rate"`
	MinConfirmations uint8                         `json:"min_confirmations"`
	SkipSync         bool                          `json:"skip_sync"`
	RecipientMap     map[string][]SendRGBRecipient `json:"recipient_map"`
}

// SendRGBResponse represents the response from /sendrgb endpoint.
type SendRGBResponse struct {
	BatchTransferIdx int64 `json:"batch_transfer_idx,omitempty"`
}

// SendRGB calls the /sendrgb endpoint.
func (c *Client) SendRGB(ctx context.Context, req SendRGBRequest) (SendRGBResponse, error) {
	var resp SendRGBResponse
	if err := c.post(ctx, "/sendrgb", req, &resp); err != nil {
		return SendRGBResponse{}, err
	}
	return resp, nil
}

// ListUnspentsRequest represents the request for /listunspents endpoint.
type ListUnspentsRequest struct {
	SkipSync    bool `json:"skip_sync"`
	SettledOnly bool `json:"settled_only"`
}

// UTXO represents a wallet UTXO.
type UTXO struct {
	Outpoint  string `json:"outpoint"`
	BtcAmount uint64 `json:"btc_amount"`
	Colorable bool   `json:"colorable"`
}

// Unspent represents an unspent output.
type Unspent struct {
	UTXO UTXO `json:"utxo"`
}

// ListUnspentsResponse represents the response from /listunspents endpoint.
type ListUnspentsResponse struct {
	Unspents []Unspent `json:"unspents"`
}

// ListUnspents calls the /listunspents endpoint.
func (c *Client) ListUnspents(ctx context.Context, req ListUnspentsRequest) (ListUnspentsResponse, error) {
	var resp ListUnspentsResponse
	if err := c.post(ctx, "/listunspents", req, &resp); err != nil {
		return ListUnspentsResponse{}, err
	}
	return resp, nil
}

// CreateUtxosRequest represents the request for /createutxos endpoint.
type CreateUtxosRequest struct {
	UpTo     bool    `json:"up_to"`
	Num      *uint8  `json:"num,omitempty"`
	Size     *uint32 `json:"size,omitempty"`
	FeeRate  uint64  `json:"fee_rate"`
	SkipSync bool    `json:"skip_sync"`
}

// CreateUtxos calls the /createutxos endpoint.
func (c *Client) CreateUtxos(ctx context.Context, req CreateUtxosRequest) error {
	var resp struct{}
	return c.post(ctx, "/createutxos", req, &resp)
}

// Sync calls the /sync endpoint to sync the RGB wallet.
func (c *Client) Sync(ctx context.Context) error {
	var resp struct{}
	return c.post(ctx, "/sync", struct{}{}, &resp)
}
