package wasmd_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/sei-protocol/sei-chain/app"
	"github.com/sei-protocol/sei-chain/precompiles/wasmd"
	sdk "github.com/sei-protocol/sei-chain/sei-cosmos/types"
	wasmkeeper "github.com/sei-protocol/sei-chain/sei-wasmd/x/wasm/keeper"
	wasmvmtypes "github.com/sei-protocol/sei-chain/sei-wasmvm/types"
	testkeeper "github.com/sei-protocol/sei-chain/testutil/keeper"
	"github.com/sei-protocol/sei-chain/x/evm/state"
	evmtypes "github.com/sei-protocol/sei-chain/x/evm/types"
	"github.com/stretchr/testify/require"
)

const candSEITxFrameGas = uint64(35_000_000)
const candSEITxFrameVariants = 620

type candSEITxFrameResult struct {
	Candidate                string               `json:"candidate"`
	Baseline                 string               `json:"baseline"`
	VariantCount             int                  `json:"variant_count"`
	PackedAddressBytes       int                  `json:"packed_address_bytes"`
	TransactionCalldataBytes int                  `json:"transaction_calldata_bytes"`
	IntrinsicPlusFloorGas    uint64               `json:"intrinsic_plus_floor_gas"`
	GasLimit                 uint64               `json:"gas_limit"`
	UsedGas                  uint64               `json:"used_gas"`
	RefundedGas              uint64               `json:"refunded_gas"`
	ExecutionError           string               `json:"execution_error,omitempty"`
	CoreError                string               `json:"core_error,omitempty"`
	ReturnBytes              int                  `json:"return_bytes"`
	DecodedCompleted         uint64               `json:"decoded_completed,omitempty"`
	ObservedFSLoads          uint64               `json:"observed_fs_loads"`
	WallNS                   int64                `json:"wall_ns"`
	CPUNS                    int64                `json:"cpu_ns"`
	RSSBeforeKB              uint64               `json:"rss_before_kb"`
	RSSAfterKB               uint64               `json:"rss_after_kb"`
	RSSDeltaKB               int64                `json:"rss_delta_kb"`
	MetricsBefore            *wasmvmtypes.Metrics `json:"metrics_before"`
	MetricsAfter             *wasmvmtypes.Metrics `json:"metrics_after"`
	WrapperRuntimeBytes      int                  `json:"wrapper_runtime_bytes"`
	SetupWallNS              int64                `json:"setup_wall_ns"`
	SetupCosmosGas           uint64               `json:"setup_cosmos_gas"`
	DistinctHashes           int                  `json:"distinct_hashes"`
	Notes                    []string             `json:"notes"`
}

func candSEITxULEB(v uint64) []byte {
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

func candSEITxVariantCode(base []byte, i int) []byte {
	name := []byte("coinsspor.cand.sei.txframe")
	marker := []byte(fmt.Sprintf("variant-%08d", i))
	payload := append(candSEITxULEB(uint64(len(name))), name...)
	payload = append(payload, marker...)
	section := []byte{0x00}
	section = append(section, candSEITxULEB(uint64(len(payload)))...)
	section = append(section, payload...)
	return append(append(append([]byte{}, base...), section...), []byte{}...)
}

func candSEITxCPU() time.Duration {
	var r syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &r); err != nil {
		return 0
	}
	return time.Duration(r.Utime.Sec+r.Stime.Sec)*time.Second + time.Duration(r.Utime.Usec+r.Stime.Usec)*time.Microsecond
}

func candSEITxStatusKB(key string) uint64 {
	bz, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(bz), "\n") {
		if !strings.HasPrefix(line, key+":") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		v, _ := strconv.ParseUint(fields[1], 10, 64)
		return v
	}
	return 0
}

func candSEITxReadHexFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	out, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	require.NoError(t, err)
	return out
}

func candSEITxWriteResult(t *testing.T, result candSEITxFrameResult) {
	t.Helper()
	dir := os.Getenv("CAND_SEI_EVIDENCE_DIR")
	if dir == "" {
		return
	}
	require.NoError(t, os.MkdirAll(dir, 0o755))
	bz, err := json.MarshalIndent(result, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "txframe-results.json"), append(bz, '\n'), 0o644))
}

func TestCANDSEIDynamicGasSingleEVMMessage(t *testing.T) {
	testApp := app.Setup(t, false, false, false)
	creator, creatorEVM := testkeeper.MockAddressPair()
	ctx := testApp.GetContextForDeliverTx([]byte("cand-sei-dynamic-txframe"))
	ctx = ctx.WithBlockTime(time.Unix(1_700_000_000, 0)).WithIsEVM(true)
	ctx = ctx.WithGasMeter(sdk.NewInfiniteGasMeterWithMultiplier(ctx))
	testApp.EvmKeeper.SetAddressMapping(ctx, creator, creatorEVM)

	baseCode, err := os.ReadFile("../../example/cosmwasm/echo/artifacts/echo.wasm")
	require.NoError(t, err)
	permissionKeeper := wasmkeeper.NewDefaultPermissionKeeper(testApp.WasmKeeper)
	setupCtx := ctx.WithIsSimulation(true).WithGasMeter(sdk.NewInfiniteGasMeterWithMultiplier(ctx))
	packedAddresses := make([]byte, 0, candSEITxFrameVariants*62)
	hashes := make(map[[32]byte]struct{}, candSEITxFrameVariants)
	setupStarted := time.Now()
	for i := 0; i < candSEITxFrameVariants; i++ {
		code := candSEITxVariantCode(baseCode, i)
		hashes[sha256.Sum256(code)] = struct{}{}
		codeID, createErr := permissionKeeper.Create(setupCtx, creator, code, nil)
		require.NoErrorf(t, createErr, "create variant %d", i)
		contract, _, instantiateErr := permissionKeeper.Instantiate(setupCtx, codeID, creator, creator, []byte("{}"), fmt.Sprintf("cand-txframe-%04d", i), sdk.NewCoins())
		require.NoErrorf(t, instantiateErr, "instantiate variant %d", i)
		require.Len(t, contract.String(), 62)
		packedAddresses = append(packedAddresses, contract.String()...)
	}
	setupWall := time.Since(setupStarted)

	wrapperRuntime := candSEITxReadHexFile(t, os.Getenv("CAND_SEI_WRAPPER_RUNTIME"))
	abiRaw, err := os.ReadFile(os.Getenv("CAND_SEI_WRAPPER_ABI"))
	require.NoError(t, err)
	wrapperABI, err := abi.JSON(bytes.NewReader(abiRaw))
	require.NoError(t, err)
	transactionData, err := wrapperABI.Pack("run", packedAddresses)
	require.NoError(t, err)

	precompile, err := wasmd.NewPrecompile(testApp.GetPrecompileKeepers())
	require.NoError(t, err)
	stateDB := state.NewDBImpl(ctx, &testApp.EvmKeeper, true)
	wrapperAddress := ethcommon.HexToAddress("0x00000000000000000000000000000000C01DCAFE")
	stateDB.SetCode(wrapperAddress, wrapperRuntime)

	blockGasPool := core.GasPool(candSEITxFrameGas)
	blockCtx, err := testApp.EvmKeeper.GetVMBlockContext(ctx, blockGasPool)
	require.NoError(t, err)
	chainConfig := evmtypes.DefaultChainConfig().EthereumConfig(testApp.EvmKeeper.ChainID(ctx))
	precompiles := map[ethcommon.Address]vm.PrecompiledContract{
		ethcommon.HexToAddress(wasmd.WasmdAddress): precompile,
	}
	evm := vm.NewEVM(*blockCtx, stateDB, chainConfig, vm.Config{NoBaseFee: true}, precompiles)

	to := wrapperAddress
	msg := &core.Message{
		To:               &to,
		From:             creatorEVM,
		Nonce:            0,
		Value:            big.NewInt(0),
		GasLimit:         candSEITxFrameGas,
		GasPrice:         big.NewInt(0),
		GasFeeCap:        big.NewInt(0),
		GasTipCap:        big.NewInt(0),
		Data:             transactionData,
		SkipNonceChecks:  true,
		SkipFromEOACheck: true,
	}
	gp := new(core.GasPool).AddGas(candSEITxFrameGas)
	metricsBefore, err := testApp.WasmKeeper.CANDSEIDebugWasmVMMetrics(ctx)
	require.NoError(t, err)
	rssBefore := candSEITxStatusKB("VmRSS")
	cpuBefore := candSEITxCPU()
	started := time.Now()
	execution, coreErr := core.ApplyMessage(evm, msg, gp)
	wall := time.Since(started)
	cpuAfter := candSEITxCPU()
	rssAfter := candSEITxStatusKB("VmRSS")
	metricsAfter, err := testApp.WasmKeeper.CANDSEIDebugWasmVMMetrics(ctx)
	require.NoError(t, err)

	intrinsic, intrinsicErr := core.IntrinsicGas(transactionData, nil, nil, false, true, true, false)
	require.NoError(t, intrinsicErr)
	floor, floorErr := core.FloorDataGas(transactionData)
	require.NoError(t, floorErr)
	if floor > intrinsic {
		intrinsic = floor
	}

	result := candSEITxFrameResult{
		Candidate:                "CAND-SEI-DYNAMIC-GAS-NONKV-001",
		Baseline:                 "f70ea4fea2dbb6145bec697e86140940ff310030",
		VariantCount:             candSEITxFrameVariants,
		PackedAddressBytes:       len(packedAddresses),
		TransactionCalldataBytes: len(transactionData),
		IntrinsicPlusFloorGas:    intrinsic,
		GasLimit:                 candSEITxFrameGas,
		WallNS:                   wall.Nanoseconds(),
		CPUNS:                    (cpuAfter - cpuBefore).Nanoseconds(),
		RSSBeforeKB:              rssBefore,
		RSSAfterKB:               rssAfter,
		RSSDeltaKB:               int64(rssAfter) - int64(rssBefore),
		MetricsBefore:            metricsBefore,
		MetricsAfter:             metricsAfter,
		WrapperRuntimeBytes:      len(wrapperRuntime),
		SetupWallNS:              setupWall.Nanoseconds(),
		SetupCosmosGas:           setupCtx.GasMeter().GasConsumed(),
		DistinctHashes:           len(hashes),
		Notes: []string{
			"A single core.ApplyMessage executes an EVM batch contract, which loops over packed bech32 addresses and STATICCALLs the production wasmd dynamic-gas precompile.",
			"The 35M message gas limit includes intrinsic/floor data gas, EVM loop work, ABI construction, call overhead, and all Wasm query gas.",
			"The setup uploads and instantiates cryptographically distinct valid echo.wasm variants on the separate simulation WasmVM so the default production cache begins cold.",
		},
	}
	if coreErr != nil {
		result.CoreError = coreErr.Error()
	}
	if execution != nil {
		result.UsedGas = execution.UsedGas
		result.RefundedGas = execution.RefundedGas
		result.ReturnBytes = len(execution.ReturnData)
		if execution.Err != nil {
			result.ExecutionError = execution.Err.Error()
		} else {
			outputs, unpackErr := wrapperABI.Methods["run"].Outputs.Unpack(execution.ReturnData)
			require.NoError(t, unpackErr)
			if len(outputs) == 1 {
				result.DecodedCompleted = outputs[0].(*big.Int).Uint64()
			}
		}
	}
	if metricsAfter.HitsFsCache >= metricsBefore.HitsFsCache {
		result.ObservedFSLoads = metricsAfter.HitsFsCache - metricsBefore.HitsFsCache
	}
	candSEITxWriteResult(t, result)

	require.NoError(t, coreErr)
	require.NotNil(t, execution)
	t.Logf("single-message result: wall=%s cpu=%s used_gas=%d intrinsic_floor=%d exec_err=%v fs_loads=%d completed=%d rss_delta_kb=%d", wall, cpuAfter-cpuBefore, execution.UsedGas, intrinsic, execution.Err, result.ObservedFSLoads, result.DecodedCompleted, result.RSSDeltaKB)
}
