package keeper

import (
	sdk "github.com/sei-protocol/sei-chain/sei-cosmos/types"
	wasmvmtypes "github.com/sei-protocol/sei-chain/sei-wasmvm/types"
)

// CANDSEIDebugWasmVMMetrics exposes the selected production WasmVM cache metrics
// for the local-only CAND-SEI-DYNAMIC-GAS-NONKV-001 benchmark.
func (k Keeper) CANDSEIDebugWasmVMMetrics(ctx sdk.Context) (*wasmvmtypes.Metrics, error) {
	return k.getWasmer(ctx).GetMetrics()
}
