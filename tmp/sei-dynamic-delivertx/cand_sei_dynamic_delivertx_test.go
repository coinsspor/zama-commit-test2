package app_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/sei-protocol/sei-chain/app"
	"github.com/sei-protocol/sei-chain/sei-cosmos/crypto/keys/secp256k1"
	sdk "github.com/sei-protocol/sei-chain/sei-cosmos/types"
	wasmkeeper "github.com/sei-protocol/sei-chain/sei-wasmd/x/wasm/keeper"
	wasmvmtypes "github.com/sei-protocol/sei-chain/sei-wasmvm/types"
	abci "github.com/sei-protocol/sei-chain/sei-tendermint/abci/types"
	tmtypes "github.com/sei-protocol/sei-chain/sei-tendermint/proto/tendermint/types"
	evmtypes "github.com/sei-protocol/sei-chain/x/evm/types"
	"github.com/stretchr/testify/require"
)

const candSEIDeliverGas = uint64(35_000_000)
const candSEIDeliverVariants = 620

type candSEIDeliverResult struct {
	Candidate                string               `json:"candidate"`
	Baseline                 string               `json:"baseline"`
	VariantCount             int                  `json:"variant_count"`
	DistinctHashes           int                  `json:"distinct_hashes"`
	PackedAddressBytes       int                  `json:"packed_address_bytes"`
	TransactionCalldataBytes int                  `json:"transaction_calldata_bytes"`
	EncodedSeiTxBytes        int                  `json:"encoded_sei_tx_bytes"`
	GasLimit                 uint64               `json:"gas_limit"`
	GasPriceWei              string               `json:"gas_price_wei"`
	BlockWallNS              int64                `json:"block_wall_ns"`
	BlockCPUNS               int64                `json:"block_cpu_ns"`
	TxResultCode             uint32               `json:"tx_result_code"`
	TxResultCodespace        string               `json:"tx_result_codespace,omitempty"`
	TxResultLog              string               `json:"tx_result_log,omitempty"`
	TxGasWanted              int64                `json:"tx_gas_wanted"`
	TxGasUsed                int64                `json:"tx_gas_used"`
	ReceiptStatus            uint32               `json:"receipt_status"`
	ReceiptGasUsed           uint64               `json:"receipt_gas_used"`
	ReceiptError             string               `json:"receipt_error,omitempty"`
	ObservedFSLoads          uint64               `json:"observed_fs_loads"`
	RSSBeforeKB              uint64               `json:"rss_before_kb"`
	RSSAfterKB               uint64               `json:"rss_after_kb"`
	RSSDeltaKB               int64                `json:"rss_delta_kb"`
	MetricsBefore            *wasmvmtypes.Metrics `json:"metrics_before"`
	MetricsAfter             *wasmvmtypes.Metrics `json:"metrics_after"`
	WrapperRuntimeBytes      int                  `json:"wrapper_runtime_bytes"`
	SetupWallNS              int64                `json:"setup_wall_ns"`
	SetupCosmosGas           uint64               `json:"setup_cosmos_gas"`
	Notes                    []string             `json:"notes"`
}

func candSEIDeliverULEB(v uint64) []byte {
	var out []byte
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			b |= 0x80
		}
		out = append(out, b)
		if v == 0 {
			return out
		}
	}
}

func candSEIDeliverVariant(base []byte, i int) []byte {
	name := []byte("coinsspor.cand.sei.delivertx")
	marker := []byte(fmt.Sprintf("variant-%08d", i))
	payload := append(candSEIDeliverULEB(uint64(len(name))), name...)
	payload = append(payload, marker...)
	section := []byte{0}
	section = append(section, candSEIDeliverULEB(uint64(len(payload)))...)
	section = append(section, payload...)
	return append(append([]byte{}, base...), section...)
}

func candSEIDeliverCPU() time.Duration {
	var r syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &r); err != nil {
		return 0
	}
	return time.Duration(r.Utime.Sec+r.Stime.Sec)*time.Second + time.Duration(r.Utime.Usec+r.Stime.Usec)*time.Microsecond
}

func candSEIDeliverStatusKB(key string) uint64 {
	bz, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(bz), "\n") {
		if !strings.HasPrefix(line, key+":") {
			continue
		}
		var v uint64
		_, _ = fmt.Sscanf(line, key+": %d kB", &v)
		return v
	}
	return 0
}

func candSEIDeliverReadHex(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	out, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	require.NoError(t, err)
	return out
}

func TestCANDSEIDynamicGasSignedDeliverTx(t *testing.T) {
	tm := time.Now().UTC()
	valPub := secp256k1.GenPrivKey().PubKey()
	testWrapper := app.NewTestWrapper(t, tm, valPub, false)
	a := testWrapper.App
	ctx := testWrapper.Ctx.WithBlockHeight(1).WithIsEVM(true)

	baseCode, err := os.ReadFile("../example/cosmwasm/echo/artifacts/echo.wasm")
	require.NoError(t, err)
	permissionKeeper := wasmkeeper.NewDefaultPermissionKeeper(a.WasmKeeper)
	setupCtx := ctx.WithIsSimulation(true).WithGasMeter(sdk.NewInfiniteGasMeterWithMultiplier(ctx))
	packedAddresses := make([]byte, 0, candSEIDeliverVariants*62)
	hashes := make(map[[32]byte]struct{}, candSEIDeliverVariants)
	setupStarted := time.Now()
	for i := 0; i < candSEIDeliverVariants; i++ {
		code := candSEIDeliverVariant(baseCode, i)
		hashes[sha256.Sum256(code)] = struct{}{}
		codeID, createErr := permissionKeeper.Create(setupCtx, sdk.AccAddress(bytes.Repeat([]byte{0x42}, 20)), code, nil)
		require.NoErrorf(t, createErr, "create variant %d", i)
		contract, _, instantiateErr := permissionKeeper.Instantiate(setupCtx, codeID, sdk.AccAddress(bytes.Repeat([]byte{0x42}, 20)), nil, []byte("{}"), fmt.Sprintf("cand-deliver-%04d", i), sdk.NewCoins())
		require.NoErrorf(t, instantiateErr, "instantiate variant %d", i)
		require.Len(t, contract.String(), 62)
		packedAddresses = append(packedAddresses, contract.String()...)
	}
	setupWall := time.Since(setupStarted)

	wrapperRuntime := candSEIDeliverReadHex(t, os.Getenv("CAND_SEI_WRAPPER_RUNTIME"))
	abiRaw, err := os.ReadFile(os.Getenv("CAND_SEI_WRAPPER_ABI"))
	require.NoError(t, err)
	wrapperABI, err := abi.JSON(bytes.NewReader(abiRaw))
	require.NoError(t, err)
	transactionData, err := wrapperABI.Pack("run", packedAddresses)
	require.NoError(t, err)
	wrapperAddress := common.HexToAddress("0x00000000000000000000000000000000C01DCAFE")
	a.EvmKeeper.SetCode(ctx, wrapperAddress, wrapperRuntime)

	sponsorKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	sponsorAddr := crypto.PubkeyToAddress(sponsorKey.PublicKey)
	fund := sdk.NewCoins(sdk.NewCoin("usei", sdk.NewInt(100_000_000)))
	require.NoError(t, a.BankKeeper.MintCoins(ctx, evmtypes.ModuleName, fund))
	require.NoError(t, a.BankKeeper.SendCoinsFromModuleToAccount(ctx, evmtypes.ModuleName, sdk.AccAddress(sponsorAddr[:]), fund))

	chainID := a.EvmKeeper.ChainID(ctx)
	gasPrice := big.NewInt(100_000_000_000)
	signedTx, err := ethtypes.SignTx(ethtypes.NewTx(&ethtypes.LegacyTx{
		Nonce:    0,
		GasPrice: gasPrice,
		Gas:      candSEIDeliverGas,
		To:       &wrapperAddress,
		Data:     transactionData,
	}), ethtypes.NewPragueSigner(chainID), sponsorKey)
	require.NoError(t, err)
	encoded := signAndEncodeEVMTx(t, a, signedTx)

	metricsBefore, err := a.WasmKeeper.CANDSEIDebugWasmVMMetrics(ctx)
	require.NoError(t, err)
	rssBefore := candSEIDeliverStatusKB("VmRSS")
	cpuBefore := candSEIDeliverCPU()
	started := time.Now()
	req := &abci.RequestFinalizeBlock{Header: &tmtypes.Header{ChainID: "sei-test", Height: 1, Time: tm}}
	_, txResults, _, processErr := a.ProcessBlock(ctx, [][]byte{encoded}, finalizeToBlockProcessReq(req), req.DecidedLastCommit, false, nil)
	wall := time.Since(started)
	cpuAfter := candSEIDeliverCPU()
	rssAfter := candSEIDeliverStatusKB("VmRSS")
	metricsAfter, err := a.WasmKeeper.CANDSEIDebugWasmVMMetrics(ctx)
	require.NoError(t, err)
	require.NoError(t, processErr)
	require.Len(t, txResults, 1)

	result := candSEIDeliverResult{
		Candidate:                "CAND-SEI-DYNAMIC-GAS-NONKV-001",
		Baseline:                 "f70ea4fea2dbb6145bec697e86140940ff310030",
		VariantCount:             candSEIDeliverVariants,
		DistinctHashes:           len(hashes),
		PackedAddressBytes:       len(packedAddresses),
		TransactionCalldataBytes: len(transactionData),
		EncodedSeiTxBytes:        len(encoded),
		GasLimit:                 candSEIDeliverGas,
		GasPriceWei:              gasPrice.String(),
		BlockWallNS:              wall.Nanoseconds(),
		BlockCPUNS:               (cpuAfter - cpuBefore).Nanoseconds(),
		TxResultCode:             txResults[0].Code,
		TxResultCodespace:        txResults[0].Codespace,
		TxResultLog:              txResults[0].Log,
		TxGasWanted:              txResults[0].GasWanted,
		TxGasUsed:                txResults[0].GasUsed,
		RSSBeforeKB:              rssBefore,
		RSSAfterKB:               rssAfter,
		RSSDeltaKB:               int64(rssAfter) - int64(rssBefore),
		MetricsBefore:            metricsBefore,
		MetricsAfter:             metricsAfter,
		WrapperRuntimeBytes:      len(wrapperRuntime),
		SetupWallNS:              setupWall.Nanoseconds(),
		SetupCosmosGas:           setupCtx.GasMeter().GasConsumed(),
		Notes: []string{
			"The measured window is one real signed Ethereum transaction encoded in a Sei tx envelope and executed through App.ProcessBlock.",
			"The outer EVM contract loops over packed Bech32 addresses and STATICCALLs the production wasmd dynamic-gas precompile.",
			"Wasm upload/instantiate preparation is outside the measured block window and uses the separate simulation WasmVM so the production cache starts cold.",
		},
	}
	if metricsAfter.HitsFsCache >= metricsBefore.HitsFsCache {
		result.ObservedFSLoads = uint64(metricsAfter.HitsFsCache - metricsBefore.HitsFsCache)
	}
	receipt, receiptErr := a.EvmKeeper.GetTransientReceipt(ctx, signedTx.Hash(), 0)
	if receiptErr != nil {
		result.ReceiptError = receiptErr.Error()
	} else if receipt != nil {
		result.ReceiptStatus = receipt.Status
		result.ReceiptGasUsed = receipt.GasUsed
	}

	dir := os.Getenv("CAND_SEI_EVIDENCE_DIR")
	if dir != "" {
		require.NoError(t, os.MkdirAll(dir, 0o755))
		bz, marshalErr := json.MarshalIndent(result, "", "  ")
		require.NoError(t, marshalErr)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "delivertx-results.json"), append(bz, '\n'), 0o644))
	}

	t.Logf("signed DeliverTx result: block_wall=%s cpu=%s tx_code=%d gas_wanted=%d gas_used=%d receipt_status=%d receipt_gas=%d fs_loads=%d rss_delta_kb=%d encoded_bytes=%d", wall, cpuAfter-cpuBefore, result.TxResultCode, result.TxGasWanted, result.TxGasUsed, result.ReceiptStatus, result.ReceiptGasUsed, result.ObservedFSLoads, result.RSSDeltaKB, result.EncodedSeiTxBytes)
}
