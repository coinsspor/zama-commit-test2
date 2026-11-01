package wasmd_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/sei-protocol/sei-chain/app"
	"github.com/sei-protocol/sei-chain/precompiles/wasmd"
	sdk "github.com/sei-protocol/sei-chain/sei-cosmos/types"
	wasmkeeper "github.com/sei-protocol/sei-chain/sei-wasmd/x/wasm/keeper"
	wasmvmtypes "github.com/sei-protocol/sei-chain/sei-wasmvm/types"
	testkeeper "github.com/sei-protocol/sei-chain/testutil/keeper"
	"github.com/sei-protocol/sei-chain/x/evm/state"
	"github.com/stretchr/testify/require"
)

const candSEIDynamicDefaultGas = uint64(1_000_000)
const candSEIDynamicBlockGasBudget = uint64(35_000_000)

type candSEIResource struct {
	WallNS      int64  `json:"wall_ns"`
	CPUNS       int64  `json:"cpu_ns"`
	TotalAlloc  uint64 `json:"total_alloc_bytes"`
	Mallocs     uint64 `json:"mallocs"`
	RSSBeforeKB uint64 `json:"rss_before_kb"`
	RSSAfterKB  uint64 `json:"rss_after_kb"`
	RSSDeltaKB  int64  `json:"rss_delta_kb"`
	HWMBeforeKB uint64 `json:"hwm_before_kb"`
	HWMAfterKB  uint64 `json:"hwm_after_kb"`
	DiskBefore  uint64 `json:"wasm_disk_before_bytes"`
	DiskAfter   uint64 `json:"wasm_disk_after_bytes"`
	DiskDelta   int64  `json:"wasm_disk_delta_bytes"`
	FilesBefore uint64 `json:"wasm_files_before"`
	FilesAfter  uint64 `json:"wasm_files_after"`
}

type candSEICall struct {
	Name                       string               `json:"name"`
	Contract                   string               `json:"contract,omitempty"`
	CodeHash                   string               `json:"code_hash,omitempty"`
	SuppliedEVMGas             uint64               `json:"supplied_evm_gas"`
	RemainingEVMGas            uint64               `json:"remaining_evm_gas"`
	UsedEVMGas                 uint64               `json:"used_evm_gas"`
	CosmosGasLimit             uint64               `json:"cosmos_gas_limit"`
	ApproxCosmosGasUsed        uint64               `json:"approx_cosmos_gas_used"`
	Err                        string               `json:"err,omitempty"`
	ErrIsOutOfGas              bool                 `json:"err_is_out_of_gas"`
	PrecompileErr              string               `json:"precompile_err,omitempty"`
	PrecompileErrIsOutOfGas    bool                 `json:"precompile_err_is_out_of_gas"`
	ReturnBytes                int                  `json:"return_bytes"`
	Resource                   candSEIResource      `json:"resource"`
	MetricsBefore              *wasmvmtypes.Metrics `json:"metrics_before,omitempty"`
	MetricsAfter               *wasmvmtypes.Metrics `json:"metrics_after,omitempty"`
}

type candSEISequence struct {
	Name             string               `json:"name"`
	Attempted        int                  `json:"attempted"`
	Succeeded        int                  `json:"succeeded"`
	Failed           int                  `json:"failed"`
	CumulativeEVMGas uint64               `json:"cumulative_evm_gas"`
	BlockGasBudget   uint64               `json:"block_gas_budget"`
	WallNS           int64                `json:"wall_ns"`
	CPUNS            int64                `json:"cpu_ns"`
	PerCallNS        []int64              `json:"per_call_ns"`
	P50NS            int64                `json:"p50_ns"`
	P95NS            int64                `json:"p95_ns"`
	MaxNS            int64                `json:"max_ns"`
	RSSBeforeKB      uint64               `json:"rss_before_kb"`
	RSSAfterKB       uint64               `json:"rss_after_kb"`
	RSSDeltaKB       int64                `json:"rss_delta_kb"`
	DiskBefore       uint64               `json:"wasm_disk_before_bytes"`
	DiskAfter        uint64               `json:"wasm_disk_after_bytes"`
	MetricsBefore    *wasmvmtypes.Metrics `json:"metrics_before,omitempty"`
	MetricsAfter     *wasmvmtypes.Metrics `json:"metrics_after,omitempty"`
}

type candSEIResults struct {
	Candidate                 string                 `json:"candidate"`
	Baseline                  string                 `json:"baseline"`
	VariantCount              int                    `json:"variant_count"`
	BaseWasmBytes             int                    `json:"base_wasm_bytes"`
	DistinctHashes            int                    `json:"distinct_hashes"`
	SetupWallNS               int64                  `json:"setup_wall_ns"`
	SetupGasConsumed          uint64                 `json:"setup_cosmos_gas_consumed"`
	SetupCompileNS            []int64                `json:"setup_compile_ns"`
	SetupInstantiateNS        []int64                `json:"setup_instantiate_ns"`
	SetupCompileP50NS         int64                  `json:"setup_compile_p50_ns"`
	SetupCompileP95NS         int64                  `json:"setup_compile_p95_ns"`
	SetupCompileMaxNS         int64                  `json:"setup_compile_max_ns"`
	WasmDiskAfterSetupBytes   uint64                 `json:"wasm_disk_after_setup_bytes"`
	WasmFilesAfterSetup       uint64                 `json:"wasm_files_after_setup"`
	MetricsDefaultAfterSetup  *wasmvmtypes.Metrics   `json:"metrics_default_after_setup,omitempty"`
	MetricsSimulationSetup    *wasmvmtypes.Metrics   `json:"metrics_simulation_after_setup,omitempty"`
	Calls                     []candSEICall          `json:"calls"`
	LateOOG                   *candSEICall           `json:"late_oog,omitempty"`
	LateOOGWarmRetry          *candSEICall           `json:"late_oog_warm_retry,omitempty"`
	OuterRevertEventsBefore   int                    `json:"outer_revert_events_before"`
	OuterRevertEventsInFrame  int                    `json:"outer_revert_events_in_frame"`
	OuterRevertEventsAfter    int                    `json:"outer_revert_events_after"`
	OuterRevertCachePersisted bool                   `json:"outer_revert_cache_persisted"`
	DirectCosmosColdNS        int64                  `json:"direct_cosmos_cold_ns"`
	DirectCosmosColdGas       uint64                 `json:"direct_cosmos_cold_gas"`
	DistinctSequence          candSEISequence        `json:"distinct_sequence"`
	DistinctSequenceRepeats   []candSEISequence      `json:"distinct_sequence_repeats,omitempty"`
	Notes                     []string               `json:"notes"`
	Environment               map[string]interface{} `json:"environment"`
}

type candSEIVariant struct {
	CodeID   uint64
	Contract sdk.AccAddress
	Hash     string
}

type candSEISnapshot struct {
	CPU       time.Duration
	Alloc     uint64
	Mallocs   uint64
	RSSKB     uint64
	HWMKB     uint64
	DiskBytes uint64
	DiskFiles uint64
}

func candSEIULEB(v uint64) []byte {
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

func candSEIVariantCode(base []byte, i int) []byte {
	name := []byte("coinsspor.cand.sei.dynamic")
	marker := []byte(fmt.Sprintf("variant-%08d", i))
	payload := append(candSEIULEB(uint64(len(name))), name...)
	payload = append(payload, marker...)
	section := []byte{0x00}
	section = append(section, candSEIULEB(uint64(len(payload)))...)
	section = append(section, payload...)
	out := make([]byte, 0, len(base)+len(section))
	out = append(out, base...)
	out = append(out, section...)
	return out
}

func candSEICPU() time.Duration {
	var r syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &r); err != nil {
		return 0
	}
	return time.Duration(r.Utime.Sec+r.Stime.Sec)*time.Second + time.Duration(r.Utime.Usec+r.Stime.Usec)*time.Microsecond
}

func candSEIProcStatusKB(key string) uint64 {
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

func candSEIWasmDisk(root string) (uint64, uint64) {
	var bytes uint64
	var files uint64
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		norm := filepath.ToSlash(path)
		if !strings.Contains(norm, "/wasm/") && !strings.Contains(norm, "/wasmvm") {
			return nil
		}
		files++
		if info.Size() > 0 {
			bytes += uint64(info.Size())
		}
		return nil
	})
	return bytes, files
}

func candSEITakeSnapshot(root string) candSEISnapshot {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	disk, files := candSEIWasmDisk(root)
	return candSEISnapshot{CPU: candSEICPU(), Alloc: m.TotalAlloc, Mallocs: m.Mallocs, RSSKB: candSEIProcStatusKB("VmRSS"), HWMKB: candSEIProcStatusKB("VmHWM"), DiskBytes: disk, DiskFiles: files}
}

func candSEIResourceDelta(before, after candSEISnapshot, wall time.Duration) candSEIResource {
	return candSEIResource{WallNS: wall.Nanoseconds(), CPUNS: (after.CPU - before.CPU).Nanoseconds(), TotalAlloc: after.Alloc - before.Alloc, Mallocs: after.Mallocs - before.Mallocs, RSSBeforeKB: before.RSSKB, RSSAfterKB: after.RSSKB, RSSDeltaKB: int64(after.RSSKB) - int64(before.RSSKB), HWMBeforeKB: before.HWMKB, HWMAfterKB: after.HWMKB, DiskBefore: before.DiskBytes, DiskAfter: after.DiskBytes, DiskDelta: int64(after.DiskBytes) - int64(before.DiskBytes), FilesBefore: before.DiskFiles, FilesAfter: after.DiskFiles}
}

func candSEIPercentile(ns []int64, p float64) int64 {
	if len(ns) == 0 {
		return 0
	}
	cp := append([]int64(nil), ns...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	idx := int(float64(len(cp)-1) * p)
	return cp[idx]
}

func candSEIIsOutOfGas(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, vm.ErrOutOfGas) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "out of gas") || strings.Contains(msg, "gas limit exceeded")
}

func candSEIWriteJSON(t *testing.T, name string, v interface{}) {
	t.Helper()
	dir := os.Getenv("CAND_SEI_EVIDENCE_DIR")
	if dir == "" {
		return
	}
	require.NoError(t, os.MkdirAll(dir, 0o755))
	bz, err := json.MarshalIndent(v, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), append(bz, '\n'), 0o644))
}

func TestCANDSEIDynamicGasNonKV001(t *testing.T) {
	variantCount := 32
	if raw := os.Getenv("CAND_SEI_VARIANTS"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 12 {
			variantCount = n
		}
	}
	tmpRoot := os.Getenv("CAND_SEI_TMP_ROOT")
	if tmpRoot == "" {
		tmpRoot = os.TempDir()
	}

	testApp := app.Setup(t, false, false, false)
	creator, creatorEVM := testkeeper.MockAddressPair()
	ctx := testApp.GetContextForDeliverTx([]byte("cand-sei-dynamic-gas-nonkv"))
	ctx = ctx.WithBlockTime(time.Unix(1_700_000_000, 0)).WithIsEVM(true)
	testApp.EvmKeeper.SetAddressMapping(ctx, creator, creatorEVM)

	permissionKeeper := wasmkeeper.NewDefaultPermissionKeeper(testApp.WasmKeeper)
	precompile, err := wasmd.NewPrecompile(testApp.GetPrecompileKeepers())
	require.NoError(t, err)
	executor := precompile.GetExecutor().(*wasmd.PrecompileExecutor)
	queryMethod, err := precompile.ABI.MethodById(executor.QueryID)
	require.NoError(t, err)
	executeMethod, err := precompile.ABI.MethodById(executor.ExecuteID)
	require.NoError(t, err)

	baseCode, err := os.ReadFile("../../example/cosmwasm/echo/artifacts/echo.wasm")
	require.NoError(t, err)

	setupCtx := ctx.WithIsSimulation(true).WithGasMeter(sdk.NewInfiniteGasMeterWithMultiplier(ctx))
	variants := make([]candSEIVariant, 0, variantCount)
	compileNS := make([]int64, 0, variantCount)
	instantiateNS := make([]int64, 0, variantCount)
	hashes := make(map[string]struct{}, variantCount)
	setupStarted := time.Now()
	for i := 0; i < variantCount; i++ {
		code := candSEIVariantCode(baseCode, i)
		h := sha256.Sum256(code)
		hash := hex.EncodeToString(h[:])
		hashes[hash] = struct{}{}

		started := time.Now()
		codeID, err := permissionKeeper.Create(setupCtx, creator, code, nil)
		compileNS = append(compileNS, time.Since(started).Nanoseconds())
		require.NoErrorf(t, err, "create variant %d", i)

		started = time.Now()
		contract, _, err := permissionKeeper.Instantiate(setupCtx, codeID, creator, creator, []byte("{}"), fmt.Sprintf("cand-dynamic-%04d", i), sdk.NewCoins())
		instantiateNS = append(instantiateNS, time.Since(started).Nanoseconds())
		require.NoErrorf(t, err, "instantiate variant %d", i)
		variants = append(variants, candSEIVariant{CodeID: codeID, Contract: contract, Hash: hash})
	}
	setupWall := time.Since(setupStarted)

	defaultMetricsAfterSetup, err := testApp.WasmKeeper.CANDSEIDebugWasmVMMetrics(ctx)
	require.NoError(t, err)
	simMetricsAfterSetup, err := testApp.WasmKeeper.CANDSEIDebugWasmVMMetrics(setupCtx)
	require.NoError(t, err)
	diskAfterSetup, filesAfterSetup := candSEIWasmDisk(tmpRoot)

	results := candSEIResults{Candidate: "CAND-SEI-DYNAMIC-GAS-NONKV-001", Baseline: "f70ea4fea2dbb6145bec697e86140940ff310030", VariantCount: variantCount, BaseWasmBytes: len(baseCode), DistinctHashes: len(hashes), SetupWallNS: setupWall.Nanoseconds(), SetupGasConsumed: setupCtx.GasMeter().GasConsumed(), SetupCompileNS: compileNS, SetupInstantiateNS: instantiateNS, SetupCompileP50NS: candSEIPercentile(compileNS, 0.50), SetupCompileP95NS: candSEIPercentile(compileNS, 0.95), SetupCompileMaxNS: candSEIPercentile(compileNS, 1.00), WasmDiskAfterSetupBytes: diskAfterSetup, WasmFilesAfterSetup: filesAfterSetup, MetricsDefaultAfterSetup: defaultMetricsAfterSetup, MetricsSimulationSetup: simMetricsAfterSetup, Environment: map[string]interface{}{"go_version": runtime.Version(), "goos": runtime.GOOS, "goarch": runtime.GOARCH, "gomaxprocs": runtime.GOMAXPROCS(0), "tmp_root": tmpRoot, "priority_normalizer": testApp.EvmKeeper.GetPriorityNormalizer(ctx).String()}}

	newEVM := func(callCtx sdk.Context) (*state.DBImpl, *vm.EVM) {
		db := state.NewDBImpl(callCtx, &testApp.EvmKeeper, true)
		return db, &vm.EVM{StateDB: db}
	}

	runQuery := func(name string, v candSEIVariant, msg []byte, supplied uint64, db *state.DBImpl, evm *vm.EVM) candSEICall {
		args, packErr := queryMethod.Inputs.Pack(v.Contract.String(), msg)
		require.NoError(t, packErr)
		db.SetPrecompileError(nil)
		mb, metricsErr := testApp.WasmKeeper.CANDSEIDebugWasmVMMetrics(db.Ctx())
		require.NoError(t, metricsErr)
		before := candSEITakeSnapshot(tmpRoot)
		started := time.Now()
		ret, remaining, callErr := precompile.RunAndCalculateGas(evm, common.Address{}, common.Address{}, append(executor.QueryID, args...), supplied, nil, nil, false, false)
		precompileErr := db.GetPrecompileError()
		wall := time.Since(started)
		after := candSEITakeSnapshot(tmpRoot)
		ma, metricsErr := testApp.WasmKeeper.CANDSEIDebugWasmVMMetrics(db.Ctx())
		require.NoError(t, metricsErr)
		used := supplied
		if remaining <= supplied {
			used = supplied - remaining
		}
		cosmosLimit := testApp.EvmKeeper.GetCosmosGasLimitFromEVMGas(ctx.WithGasMeter(sdk.NewInfiniteGasMeterWithMultiplier(ctx)), supplied)
		cosmosRemain := testApp.EvmKeeper.GetCosmosGasLimitFromEVMGas(ctx.WithGasMeter(sdk.NewInfiniteGasMeterWithMultiplier(ctx)), remaining)
		cosmosUsed := cosmosLimit
		if cosmosRemain <= cosmosLimit {
			cosmosUsed = cosmosLimit - cosmosRemain
		}
		out := candSEICall{Name: name, Contract: v.Contract.String(), CodeHash: v.Hash, SuppliedEVMGas: supplied, RemainingEVMGas: remaining, UsedEVMGas: used, CosmosGasLimit: cosmosLimit, ApproxCosmosGasUsed: cosmosUsed, ErrIsOutOfGas: candSEIIsOutOfGas(callErr), ReturnBytes: len(ret), Resource: candSEIResourceDelta(before, after, wall), MetricsBefore: mb, MetricsAfter: ma}
		if callErr != nil {
			out.Err = callErr.Error()
		}
		if precompileErr != nil {
			out.PrecompileErr = precompileErr.Error()
			out.PrecompileErrIsOutOfGas = candSEIIsOutOfGas(precompileErr)
		}
		return out
	}

	db, evm := newEVM(ctx)
	cold := runQuery("cold_query_success", variants[0], []byte(`{"info":{}}`), candSEIDynamicDefaultGas, db, evm)
	require.Empty(t, cold.Err)
	warm := runQuery("warm_query_success", variants[0], []byte(`{"info":{}}`), candSEIDynamicDefaultGas, db, evm)
	require.Empty(t, warm.Err)
	results.Calls = append(results.Calls, cold, warm)

	revertCall := runQuery("cold_query_contract_revert", variants[1], []byte(`{"bad":{}}`), candSEIDynamicDefaultGas, db, evm)
	require.NotEmpty(t, revertCall.Err)
	revertRetry := runQuery("post_revert_valid_retry", variants[1], []byte(`{"info":{}}`), candSEIDynamicDefaultGas, db, evm)
	require.Empty(t, revertRetry.Err)
	results.Calls = append(results.Calls, revertCall, revertRetry)

	usedReference := cold.UsedEVMGas
	candidates := []uint64{}
	for _, delta := range []uint64{1, 10, 100, 1_000, 5_000, 10_000} {
		if usedReference > delta {
			candidates = append(candidates, usedReference-delta)
		}
	}
	if usedReference > 0 {
		candidates = append(candidates, usedReference*95/100, usedReference*90/100, usedReference*80/100)
	}
	lateOOGIndex := -1
	probeIndex := 2
	for _, gas := range candidates {
		if probeIndex >= len(variants)-4 || gas == 0 {
			break
		}
		probe := runQuery(fmt.Sprintf("cold_oog_probe_%d", gas), variants[probeIndex], []byte(`{"info":{}}`), gas, db, evm)
		results.Calls = append(results.Calls, probe)
		cacheTouched := probe.MetricsAfter.HitsFsCache > probe.MetricsBefore.HitsFsCache || probe.MetricsAfter.Misses > probe.MetricsBefore.Misses || probe.MetricsAfter.ElementsMemoryCache > probe.MetricsBefore.ElementsMemoryCache
		if (probe.ErrIsOutOfGas || probe.PrecompileErrIsOutOfGas) && cacheTouched {
			lateOOGIndex = probeIndex
			copyProbe := probe
			results.LateOOG = &copyProbe
			break
		}
		probeIndex++
	}
	if lateOOGIndex >= 0 {
		retry := runQuery("post_oog_valid_retry", variants[lateOOGIndex], []byte(`{"info":{}}`), candSEIDynamicDefaultGas, db, evm)
		require.Empty(t, retry.Err)
		results.LateOOGWarmRetry = &retry
	}

	outerIndex := probeIndex + 1
	if outerIndex >= len(variants)-2 {
		outerIndex = len(variants) - 3
	}
	execArgs, err := executeMethod.Inputs.Pack(variants[outerIndex].Contract.String(), []byte(`{"echo":{"message":"outer-revert"}}`), []byte(`[]`))
	require.NoError(t, err)
	outerDB, outerEVM := newEVM(ctx)
	metricsBeforeOuter, err := testApp.WasmKeeper.CANDSEIDebugWasmVMMetrics(outerDB.Ctx())
	require.NoError(t, err)
	results.OuterRevertEventsBefore = len(outerDB.Ctx().EventManager().Events())
	rev := outerDB.Snapshot()
	_, _, err = precompile.RunAndCalculateGas(outerEVM, creatorEVM, creatorEVM, append(executor.ExecuteID, execArgs...), candSEIDynamicDefaultGas, nil, nil, false, false)
	require.NoError(t, err)
	results.OuterRevertEventsInFrame = len(outerDB.Ctx().EventManager().Events())
	outerDB.RevertToSnapshot(rev)
	results.OuterRevertEventsAfter = len(outerDB.Ctx().EventManager().Events())
	metricsAfterOuter, err := testApp.WasmKeeper.CANDSEIDebugWasmVMMetrics(outerDB.Ctx())
	require.NoError(t, err)
	results.OuterRevertCachePersisted = metricsAfterOuter.ElementsMemoryCache > metricsBeforeOuter.ElementsMemoryCache || metricsAfterOuter.HitsFsCache > metricsBeforeOuter.HitsFsCache || metricsAfterOuter.Misses > metricsBeforeOuter.Misses

	directIndex := outerIndex + 1
	directCtx := ctx.WithGasMeter(sdk.NewInfiniteGasMeterWithMultiplier(ctx))
	directStartGas := directCtx.GasMeter().GasConsumed()
	directStarted := time.Now()
	_, err = testApp.WasmKeeper.QuerySmartSafe(directCtx, variants[directIndex].Contract, []byte(`{"info":{}}`))
	results.DirectCosmosColdNS = time.Since(directStarted).Nanoseconds()
	results.DirectCosmosColdGas = directCtx.GasMeter().GasConsumed() - directStartGas
	require.NoError(t, err)

	seqStart := directIndex + 1
	maxCalls := int(candSEIDynamicBlockGasBudget / cold.UsedEVMGas)
	require.Greater(t, maxCalls, 0)
	require.GreaterOrEqual(t, len(variants)-seqStart, maxCalls)

	runSequence := func(name string) candSEISequence {
		runtime.GC()
		seqDB, seqEVM := newEVM(ctx)
		seqMetricsBefore, metricsErr := testApp.WasmKeeper.CANDSEIDebugWasmVMMetrics(seqDB.Ctx())
		require.NoError(t, metricsErr)
		seqDiskBefore, _ := candSEIWasmDisk(tmpRoot)
		seqRSSBefore := candSEIProcStatusKB("VmRSS")
		seqCPUBefore := candSEICPU()
		seqStarted := time.Now()
		seq := candSEISequence{Name: name, BlockGasBudget: candSEIDynamicBlockGasBudget}
		for i := seqStart; i < seqStart+maxCalls; i++ {
			if seq.CumulativeEVMGas+cold.UsedEVMGas > candSEIDynamicBlockGasBudget {
				break
			}
			args, packErr := queryMethod.Inputs.Pack(variants[i].Contract.String(), []byte(`{"info":{}}`))
			require.NoError(t, packErr)
			seqDB.SetPrecompileError(nil)
			callStarted := time.Now()
			_, remaining, callErr := precompile.RunAndCalculateGas(seqEVM, common.Address{}, common.Address{}, append(executor.QueryID, args...), candSEIDynamicDefaultGas, nil, nil, false, false)
			elapsed := time.Since(callStarted)
			used := candSEIDynamicDefaultGas
			if remaining <= candSEIDynamicDefaultGas {
				used -= remaining
			}
			seq.Attempted++
			seq.CumulativeEVMGas += used
			seq.PerCallNS = append(seq.PerCallNS, elapsed.Nanoseconds())
			if callErr != nil {
				seq.Failed++
			} else {
				seq.Succeeded++
			}
		}
		seq.WallNS = time.Since(seqStarted).Nanoseconds()
		seq.CPUNS = (candSEICPU() - seqCPUBefore).Nanoseconds()
		seq.RSSBeforeKB = seqRSSBefore
		seq.RSSAfterKB = candSEIProcStatusKB("VmRSS")
		seq.RSSDeltaKB = int64(seq.RSSAfterKB) - int64(seq.RSSBeforeKB)
		seq.DiskBefore = seqDiskBefore
		seq.DiskAfter, _ = candSEIWasmDisk(tmpRoot)
		seq.MetricsBefore = seqMetricsBefore
		seq.MetricsAfter, metricsErr = testApp.WasmKeeper.CANDSEIDebugWasmVMMetrics(seqDB.Ctx())
		require.NoError(t, metricsErr)
		seq.P50NS = candSEIPercentile(seq.PerCallNS, 0.50)
		seq.P95NS = candSEIPercentile(seq.PerCallNS, 0.95)
		seq.MaxNS = candSEIPercentile(seq.PerCallNS, 1.00)
		return seq
	}

	seq := runSequence("distinct_hash_queries_under_35m_evm_gas_cycle_1")
	results.DistinctSequence = seq
	for cycle := 2; cycle <= 4; cycle++ {
		results.DistinctSequenceRepeats = append(results.DistinctSequenceRepeats, runSequence(fmt.Sprintf("distinct_hash_queries_under_35m_evm_gas_cycle_%d", cycle)))
	}

	results.Notes = append(results.Notes,
		"Code upload/compilation and setup use the real permission keeper on the simulation WasmVM; all measured precompile calls use the separate default production WasmVM cache sharing the same on-disk artifacts.",
		"Appended custom sections are valid WebAssembly and preserve executable semantics while forcing distinct checksums/cache keys.",
		"A persistent cache entry after revert/OOG is expected process-local behavior and is not itself a bounty finding; listed impact still must be demonstrated.",
		"Four fresh-StateDB 35M-gas cycles scan the same distinct hash set to test cache-eviction repeatability and cumulative process RSS behavior.",
	)

	candSEIWriteJSON(t, "results.json", results)
	t.Logf("CAND-SEI-DYNAMIC result: variants=%d distinct=%d setup=%s cold=%s warm=%s sequence_calls=%d sequence_wall=%s sequence_gas=%d rss_delta_kb=%d late_oog=%v outer_events=%d/%d/%d cache_persisted=%v", results.VariantCount, results.DistinctHashes, time.Duration(results.SetupWallNS), time.Duration(cold.Resource.WallNS), time.Duration(warm.Resource.WallNS), seq.Attempted, time.Duration(seq.WallNS), seq.CumulativeEVMGas, seq.RSSDeltaKB, results.LateOOG != nil, results.OuterRevertEventsBefore, results.OuterRevertEventsInFrame, results.OuterRevertEventsAfter, results.OuterRevertCachePersisted)
}
