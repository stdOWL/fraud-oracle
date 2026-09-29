// SPDX-License-Identifier: MIT
pragma solidity ^0.8.26;

import {Script, console} from "forge-std/Script.sol";
import {FraudRegistry} from "../src/FraudRegistry.sol";

/// Usage (from contracts/):
///   forge script script/Deploy.s.sol --rpc-url sepolia --broadcast
/// Env: DEPLOYER_PRIVATE_KEY (0x-prefixed), FORWARDER_ADDRESS (MockKeystoneForwarder for simulate,
/// KeystoneForwarder for production; see Forwarder Directory in CRE docs).
contract Deploy is Script {
  function run() external returns (FraudRegistry registry) {
    uint256 pk = vm.envUint("DEPLOYER_PRIVATE_KEY");
    address forwarder = vm.envAddress("FORWARDER_ADDRESS");

    vm.startBroadcast(pk);
    registry = new FraudRegistry(forwarder);
    vm.stopBroadcast();

    console.log("FraudRegistry deployed at", address(registry));
    console.log("forwarder", forwarder);
  }
}
