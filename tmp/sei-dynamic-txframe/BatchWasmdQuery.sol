// SPDX-License-Identifier: MIT
pragma solidity 0.8.30;

contract BatchWasmdQuery {
    address internal constant WASMD = 0x0000000000000000000000000000000000001002;
    bytes4 internal constant QUERY_SELECTOR = bytes4(keccak256("query(string,bytes)"));
    uint256 internal constant BECH32_LEN = 62;

    function run(bytes calldata packedAddresses) external returns (uint256 completed) {
        require(packedAddresses.length % BECH32_LEN == 0, "bad packed length");
        bytes memory request = hex"7b22696e666f223a7b7d7d"; // {"info":{}}

        for (uint256 offset; offset < packedAddresses.length; offset += BECH32_LEN) {
            string memory target = string(packedAddresses[offset:offset + BECH32_LEN]);
            (bool ok,) = WASMD.staticcall(
                abi.encodeWithSelector(QUERY_SELECTOR, target, request)
            );
            if (!ok) {
                assembly ("memory-safe") {
                    returndatacopy(0, 0, returndatasize())
                    revert(0, returndatasize())
                }
            }
            unchecked {
                ++completed;
            }
        }
    }
}
