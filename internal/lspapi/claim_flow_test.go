package lspapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type claimFlowRoundTripper struct {
	t               *testing.T
	height          uint32
	heightValue     *atomic.Uint32
	sendCalls       *atomic.Int32
	claimCalls      *atomic.Int32
	paymentHash     string
	payeePubkey     string
	descriptionHash string
	amountMsat      uint64
	expirySec       uint32
	minFinalCltv    uint16
}

func (rt *claimFlowRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.t.Helper()

	var body any
	switch req.URL.Path {
	case "/networkinfo":
		require.Equal(rt.t, http.MethodGet, req.Method)
		height := rt.height
		if rt.heightValue != nil {
			height = rt.heightValue.Load()
		}
		body = map[string]any{"height": height}
	case "/apay/outboundinvoice":
		require.Equal(rt.t, http.MethodPost, req.Method)
		body = map[string]any{
			"bolt11":       "lnbc1claimflowoutbound",
			"payment_hash": rt.paymentHash,
		}
	case "/decodelninvoice":
		require.Equal(rt.t, http.MethodPost, req.Method)
		body = map[string]any{
			"amt_msat":                    rt.amountMsat,
			"payment_hash":                rt.paymentHash,
			"description_hash":            rt.descriptionHash,
			"payee_pubkey":                rt.payeePubkey,
			"expiry_sec":                  rt.expirySec,
			"min_final_cltv_expiry_delta": rt.minFinalCltv,
			"timestamp":                   time.Now().UTC().Unix(),
		}
	case "/sendpayment":
		require.Equal(rt.t, http.MethodPost, req.Method)
		if rt.sendCalls != nil {
			rt.sendCalls.Add(1)
		}
		body = map[string]any{"ok": true}
	case "/claimhodlinvoice":
		require.Equal(rt.t, http.MethodPost, req.Method)
		if rt.claimCalls != nil {
			rt.claimCalls.Add(1)
		}
		body = map[string]any{"ok": true}
	default:
		require.Failf(rt.t, "unexpected path", "unexpected path %s", req.URL.Path)
		body = map[string]any{"error": "unexpected path"}
	}

	buf, err := json.Marshal(body)
	require.NoError(rt.t, err)

	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     http.StatusText(http.StatusOK),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(buf))),
		Request:    req,
	}, nil
}

func newClaimFlowStubClient(t *testing.T, height uint32, heightValue *atomic.Uint32, sendCalls, claimCalls *atomic.Int32, paymentHash, payeePubkey, descriptionHash string, amountMsat uint64, expirySec uint32, minFinalCltv uint16) *NodeClient {
	t.Helper()

	return &NodeClient{
		baseURL: "http://claim-flow-stub",
		http: &http.Client{
			Transport: &claimFlowRoundTripper{
				t:               t,
				height:          height,
				heightValue:     heightValue,
				sendCalls:       sendCalls,
				claimCalls:      claimCalls,
				paymentHash:     paymentHash,
				payeePubkey:     payeePubkey,
				descriptionHash: descriptionHash,
				amountMsat:      amountMsat,
				expirySec:       expirySec,
				minFinalCltv:    minFinalCltv,
			},
		},
	}
}

func newClaimFlowTestAPI(t *testing.T, height uint32, sendCalls, claimCalls *atomic.Int32, paymentHash, payeePubkey, descriptionHash string, amountMsat uint64) (*API, *SQLStore) {
	return newClaimFlowTestAPIWithHeightSource(t, height, nil, sendCalls, claimCalls, paymentHash, payeePubkey, descriptionHash, amountMsat)
}

func newClaimFlowTestAPIWithHeightSource(t *testing.T, height uint32, heightValue *atomic.Uint32, sendCalls, claimCalls *atomic.Int32, paymentHash, payeePubkey, descriptionHash string, amountMsat uint64) (*API, *SQLStore) {
	t.Helper()

	store := newAsyncOrderTestStore(t)
	expirySec := uint32(defaultAPayOutboundInvoiceExpiry.Seconds())
	minFinalCltv := defaultAPayOutboundMinFinalCltvExpiryDelta
	rgbClient := newClaimFlowStubClient(t, height, heightValue, nil, nil, paymentHash, payeePubkey, descriptionHash, amountMsat, expirySec, minFinalCltv)
	lspClient := newClaimFlowStubClient(t, height, heightValue, sendCalls, claimCalls, paymentHash, payeePubkey, descriptionHash, amountMsat, expirySec, minFinalCltv)

	return &API{
		cfg: Config{
			HTTPTimeout:                         time.Second,
			APayBearerToken:                     "secret",
			LightningAddressDomainURL:           "https://example.com",
			LightningAddressShortDescription:    "Payment to txalkan",
			BlockHeightInfoPath:                 "/networkinfo",
			SendLNPath:                          "/sendpayment",
			APayRequestOutboundInvoicePath:      "/apay/outboundinvoice",
			DecodeLNPath:                        "/decodelninvoice",
			APayOutboundInvoiceExpiry:           defaultAPayOutboundInvoiceExpiry,
			APayOutboundMinFinalCltvExpiryDelta: defaultAPayOutboundMinFinalCltvExpiryDelta,
			APayClaimMarginBlocks:               defaultAPayClaimMarginBlocks,
		},
		db:        store,
		lspClient: lspClient,
		rgbClient: rgbClient,
	}, store
}

func prepareClaimFlowOutboundPending(t *testing.T, store *SQLStore, seed string, claimDeadlineHeight uint32) (AsyncRotatingInvoice, string) {
	t.Helper()

	paymentHash, _ := paymentHashAndPreimageForTest(seed)
	reserved := reserveAndFinalizeAsyncInvoiceForTest(
		t,
		store,
		"02"+strings.Repeat(seed, 64),
		"user"+seed,
		paymentHash,
		3_000_000,
	)

	ctx := context.Background()
	transitioned, err := store.MarkAsyncRotatingInvoiceClaimable(ctx, reserved.PaymentHash, 3_000_000, &claimDeadlineHeight)
	require.NoError(t, err)
	require.True(t, transitioned)
	transitioned, err = store.MarkAsyncRotatingInvoiceOutboundRequested(ctx, reserved.PaymentHash)
	require.NoError(t, err)
	require.True(t, transitioned)
	transitioned, err = store.MarkAsyncRotatingInvoiceOutboundPending(ctx, reserved.PaymentHash, "lnbc1outbound"+seed)
	require.NoError(t, err)
	require.True(t, transitioned)

	return reserved, paymentHash
}

func waitForAsyncInvoiceStatus(t *testing.T, store *SQLStore, paymentHash string, want AsyncInvoiceStatus) AsyncRotatingInvoice {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		current, err := store.LoadAsyncRotatingInvoiceByPaymentHash(context.Background(), paymentHash)
		require.NoError(t, err)

		if current.Status == want {
			return current
		}

		time.Sleep(10 * time.Millisecond)
	}

	current, err := store.LoadAsyncRotatingInvoiceByPaymentHash(context.Background(), paymentHash)
	require.NoError(t, err)
	require.Equal(t, want, current.Status)

	return current
}

func simulateOutboundPaymentSent(t *testing.T, api *API, paymentHash, paymentPreimage string) {
	t.Helper()

	body := strings.NewReader(`{"payment_hash":"` + paymentHash + `","payment_preimage":"` + paymentPreimage + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/internal/async_order/payment_sent", body)
	req.Header.Set("Authorization", "Bearer "+api.cfg.APayBearerToken)
	rr := httptest.NewRecorder()

	api.handleInternalAsyncOrderPaymentSent(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
}

func descriptionHashForClaimFlow(t *testing.T, username string) string {
	t.Helper()

	metadata := fmt.Sprintf(`[["text/identifier","%s@example.com"],["text/plain","Payment to txalkan"]]`, username)
	sum := sha256.Sum256([]byte(metadata))
	return hex.EncodeToString(sum[:])
}

func prepareClaimFlowOutboundClaimed(t *testing.T, store *SQLStore, seed string, claimDeadlineHeight uint32) (AsyncRotatingInvoice, string) {
	t.Helper()

	paymentHash, paymentPreimage := paymentHashAndPreimageForTest(seed)
	reserved := reserveAndFinalizeAsyncInvoiceForTest(
		t,
		store,
		"02"+strings.Repeat(seed, 64),
		"user"+seed,
		paymentHash,
		3_000_000,
	)

	ctx := context.Background()
	transitioned, err := store.MarkAsyncRotatingInvoiceClaimable(ctx, reserved.PaymentHash, 3_000_000, &claimDeadlineHeight)
	require.NoError(t, err)
	require.True(t, transitioned)
	transitioned, err = store.MarkAsyncRotatingInvoiceOutboundRequested(ctx, reserved.PaymentHash)
	require.NoError(t, err)
	require.True(t, transitioned)
	transitioned, err = store.MarkAsyncRotatingInvoiceOutboundPending(ctx, reserved.PaymentHash, "lnbc1outbound"+seed)
	require.NoError(t, err)
	require.True(t, transitioned)
	transitioned, err = store.MarkAsyncRotatingInvoiceOutboundPaid(ctx, reserved.PaymentHash)
	require.NoError(t, err)
	require.True(t, transitioned)
	transitioned, err = store.MarkAsyncRotatingInvoiceOutboundClaimed(ctx, reserved.PaymentHash, paymentPreimage)
	require.NoError(t, err)
	require.True(t, transitioned)

	return reserved, paymentPreimage
}

func TestSendOutboundPaymentCancelsOnExpiredDeadline(t *testing.T) {
	var sendCalls atomic.Int32
	const username = "user5"
	paymentHash, _ := paymentHashAndPreimageForTest("5")

	api, store := newClaimFlowTestAPI(t, 500, &sendCalls, nil, paymentHash, "02"+strings.Repeat("5", 64), descriptionHashForClaimFlow(t, username), 3_000_000)
	reserved, _ := prepareClaimFlowOutboundPending(t, store, "5", 100)

	err := api.aPaySendOutboundPaymentJob(context.Background(), reserved.PaymentHash)
	require.NoError(t, err)
	require.Zero(t, sendCalls.Load())

	current, err := store.LoadAsyncRotatingInvoiceByPaymentHash(context.Background(), reserved.PaymentHash)
	require.NoError(t, err)
	require.Equal(t, asyncInvoiceStatusOutboundCancelled, current.Status)
}

func TestSendOutboundPaymentIsIdempotent(t *testing.T) {
	var sendCalls atomic.Int32
	const username = "user6"
	paymentHash, _ := paymentHashAndPreimageForTest("6")

	api, store := newClaimFlowTestAPI(t, 1, &sendCalls, nil, paymentHash, "02"+strings.Repeat("6", 64), descriptionHashForClaimFlow(t, username), 3_000_000)
	reserved, _ := prepareClaimFlowOutboundPending(t, store, "6", 100)

	require.NoError(t, api.aPaySendOutboundPaymentJob(context.Background(), reserved.PaymentHash))
	require.NoError(t, api.aPaySendOutboundPaymentJob(context.Background(), reserved.PaymentHash))
	require.EqualValues(t, 1, sendCalls.Load())

	current, err := store.LoadAsyncRotatingInvoiceByPaymentHash(context.Background(), reserved.PaymentHash)
	require.NoError(t, err)
	require.Equal(t, asyncInvoiceStatusOutboundPaid, current.Status)
}

func TestClaimInboundCancelsAfterOutboundPaidWhenDeadlineExpired(t *testing.T) {
	var claimCalls atomic.Int32
	const username = "user7"
	paymentHash, _ := paymentHashAndPreimageForTest("7")

	api, store := newClaimFlowTestAPI(t, 500, nil, &claimCalls, paymentHash, "02"+strings.Repeat("7", 64), descriptionHashForClaimFlow(t, username), 3_000_000)
	reserved, _ := prepareClaimFlowOutboundClaimed(t, store, "7", 100)

	err := api.aPayClaimInboundInvoiceJob(context.Background(), reserved.PaymentHash)
	require.NoError(t, err)
	require.Zero(t, claimCalls.Load())

	current, err := store.LoadAsyncRotatingInvoiceByPaymentHash(context.Background(), reserved.PaymentHash)
	require.NoError(t, err)
	require.Equal(t, asyncInvoiceStatusFailed, current.Status)
}

func TestOutboxWorkerCompletesClaimFlow(t *testing.T) {
	var (
		sendCalls  atomic.Int32
		claimCalls atomic.Int32
	)

	paymentHash, paymentPreimage := paymentHashAndPreimageForTest("8")
	peerPubkey := "02" + strings.Repeat("8", 64)
	username := "user8"
	api, store := newClaimFlowTestAPI(
		t,
		1,
		&sendCalls,
		&claimCalls,
		paymentHash,
		peerPubkey,
		descriptionHashForClaimFlow(t, username),
		3_000_000,
	)

	reserved := reserveAndFinalizeAsyncInvoiceForTest(t, store, peerPubkey, username, paymentHash, 3_000_000)
	claimDeadlineHeight := uint32(100)
	transitioned, err := store.MarkAsyncRotatingInvoiceClaimable(context.Background(), reserved.PaymentHash, 3_000_000, &claimDeadlineHeight)
	require.NoError(t, err)
	require.True(t, transitioned)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				api.runAsyncOrderOutbox(ctx)
			}
		}
	}()

	// check whether worker iterate the invoice status from claimable to the outbound paid status.
	waitForAsyncInvoiceStatus(t, store, reserved.PaymentHash, asyncInvoiceStatusOutboundPaid)
	require.EqualValues(t, 1, sendCalls.Load())
	require.Zero(t, claimCalls.Load())

	// Simulate the recipient-side payment_sent callback that reveals the preimage.
	simulateOutboundPaymentSent(t, api, reserved.PaymentHash, paymentPreimage)

	// waiting for inbound claimed.
	final := waitForAsyncInvoiceStatus(t, store, reserved.PaymentHash, asyncInvoiceStatusInboundClaimed)
	require.NotNil(t, final.PaymentPreimage)
	require.Equal(t, paymentPreimage, *final.PaymentPreimage)
	require.EqualValues(t, 1, sendCalls.Load())
	require.EqualValues(t, 1, claimCalls.Load())

	cancel()
	<-done
}

func TestClaimFlowExpiresBetweenOutboundRequestAndInboundClaim(t *testing.T) {
	var (
		height     atomic.Uint32
		sendCalls  atomic.Int32
		claimCalls atomic.Int32
	)
	height.Store(1)

	paymentHash, paymentPreimage := paymentHashAndPreimageForTest("9")
	peerPubkey := "02" + strings.Repeat("9", 64)
	username := "user9"
	api, store := newClaimFlowTestAPIWithHeightSource(
		t,
		1,
		&height,
		&sendCalls,
		&claimCalls,
		paymentHash,
		peerPubkey,
		descriptionHashForClaimFlow(t, username),
		3_000_000,
	)

	reserved := reserveAndFinalizeAsyncInvoiceForTest(t, store, peerPubkey, username, paymentHash, 3_000_000)
	claimDeadlineHeight := uint32(100)
	transitioned, err := store.MarkAsyncRotatingInvoiceClaimable(context.Background(), reserved.PaymentHash, 3_000_000, &claimDeadlineHeight)
	require.NoError(t, err)
	require.True(t, transitioned)

	require.NoError(t, api.aPayRequestOutboundInvoiceJob(context.Background(), reserved.PaymentHash))
	current, err := store.LoadAsyncRotatingInvoiceByPaymentHash(context.Background(), reserved.PaymentHash)
	require.NoError(t, err)
	require.Equal(t, asyncInvoiceStatusOutboundPending, current.Status)

	require.NoError(t, api.aPaySendOutboundPaymentJob(context.Background(), reserved.PaymentHash))
	current, err = store.LoadAsyncRotatingInvoiceByPaymentHash(context.Background(), reserved.PaymentHash)
	require.NoError(t, err)
	require.Equal(t, asyncInvoiceStatusOutboundPaid, current.Status)
	require.EqualValues(t, 1, sendCalls.Load())

	simulateOutboundPaymentSent(t, api, reserved.PaymentHash, paymentPreimage)

	height.Store(claimDeadlineHeight)

	// state machine transition fails because deadline has expired (current_block_height == deadline_block_height)
	require.NoError(t, api.aPayClaimInboundInvoiceJob(context.Background(), reserved.PaymentHash))
	require.Zero(t, claimCalls.Load())

	current, err = store.LoadAsyncRotatingInvoiceByPaymentHash(context.Background(), reserved.PaymentHash)
	require.NoError(t, err)
	require.Equal(t, asyncInvoiceStatusFailed, current.Status)
	require.NotNil(t, current.PaymentPreimage)
	require.Equal(t, paymentPreimage, *current.PaymentPreimage)
}
