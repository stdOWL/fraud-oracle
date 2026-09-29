// SPDX-License-Identifier: MIT
pragma solidity ^0.8.0;

import {IERC165} from "@openzeppelin/contracts/utils/introspection/IERC165.sol";

/// @title IReceiver - receives keystone reports
/// @notice Copied from Chainlink CRE docs (Building Consumer Contracts). The KeystoneForwarder
/// calls onReport after verifying DON signatures.
interface IReceiver is IERC165 {
  /// @param metadata Workflow identity: abi.encodePacked(workflowId, workflowName, workflowOwner) (+2 bytes reportId in prod)
  /// @param report ABI-encoded workflow payload
  function onReport(bytes calldata metadata, bytes calldata report) external;
}
