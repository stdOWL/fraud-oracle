// SPDX-License-Identifier: MIT
pragma solidity ^0.8.26;

import {ReceiverTemplate} from "./cre/ReceiverTemplate.sol";

/// @title FraudRegistry - on-chain record of addresses flagged by the CRE fraud workflow
/// @notice Writes arrive only through the Chainlink KeystoneForwarder (see ReceiverTemplate).
///         A CRE workflow cannot call `flag` directly; it submits a signed report and the
///         forwarder calls `onReport`, which decodes a FlagReport and stores it.
contract FraudRegistry is ReceiverTemplate {
  /// @dev Payload the workflow ABI-encodes into the report.
  struct FlagReport {
    address subject;
    uint8 score; // 0..100
    uint32 ruleBitmask; // bit0 peel_chain, bit1 sanctions_proximity
  }

  struct Flag {
    uint8 score;
    uint32 ruleBitmask;
    uint64 flaggedAt; // block.timestamp of the latest report; 0 = never flagged
  }

  mapping(address => Flag) private s_flags;

  event Flagged(address indexed subject, uint8 score, uint32 ruleBitmask);

  error DirectFlagNotAllowed();
  error InvalidScore(uint8 score);

  constructor(address forwarder) ReceiverTemplate(forwarder) {}

  /// @notice Exists so `cre generate-bindings` sees the FlagReport struct in a public function
  ///         and emits `WriteReportFromFlagReport`. Always reverts: flags come through onReport.
  function flag(FlagReport calldata) external pure {
    revert DirectFlagNotAllowed();
  }

  function isFlagged(address subject) external view returns (bool) {
    return s_flags[subject].flaggedAt != 0;
  }

  function getFlag(address subject) external view returns (Flag memory) {
    return s_flags[subject];
  }

  /// @dev Called by ReceiverTemplate.onReport after the forwarder check.
  function _processReport(bytes calldata report) internal override {
    FlagReport memory r = abi.decode(report, (FlagReport));
    if (r.score > 100) revert InvalidScore(r.score);
    s_flags[r.subject] = Flag({score: r.score, ruleBitmask: r.ruleBitmask, flaggedAt: uint64(block.timestamp)});
    emit Flagged(r.subject, r.score, r.ruleBitmask);
  }
}
