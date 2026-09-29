// SPDX-License-Identifier: MIT
pragma solidity ^0.8.26;

import {Test} from "forge-std/Test.sol";
import {FraudRegistry} from "../src/FraudRegistry.sol";
import {ReceiverTemplate} from "../src/cre/ReceiverTemplate.sol";
import {IReceiver} from "../src/cre/IReceiver.sol";
import {IERC165} from "@openzeppelin/contracts/utils/introspection/IERC165.sol";

contract FraudRegistryTest is Test {
  FraudRegistry registry;
  address forwarder = makeAddr("forwarder");
  address stranger = makeAddr("stranger");
  address subject = makeAddr("subject");

  event Flagged(address indexed subject, uint8 score, uint32 ruleBitmask);

  function setUp() public {
    registry = new FraudRegistry(forwarder);
  }

  function _report(address a, uint8 score, uint32 mask) internal pure returns (bytes memory) {
    return abi.encode(FraudRegistry.FlagReport({subject: a, score: score, ruleBitmask: mask}));
  }

  function test_forwarderCanFlag() public {
    vm.warp(1_700_000_000);
    vm.expectEmit(true, false, false, true);
    emit Flagged(subject, 85, 3);
    vm.prank(forwarder);
    registry.onReport("", _report(subject, 85, 3));

    assertTrue(registry.isFlagged(subject));
    FraudRegistry.Flag memory f = registry.getFlag(subject);
    assertEq(f.score, 85);
    assertEq(f.ruleBitmask, 3);
    assertEq(f.flaggedAt, 1_700_000_000);
  }

  function test_strangerCannotFlag() public {
    vm.prank(stranger);
    vm.expectRevert(abi.encodeWithSelector(ReceiverTemplate.InvalidSender.selector, stranger, forwarder));
    registry.onReport("", _report(subject, 85, 3));
    assertFalse(registry.isFlagged(subject));
  }

  function test_directFlagReverts() public {
    vm.expectRevert(FraudRegistry.DirectFlagNotAllowed.selector);
    registry.flag(FraudRegistry.FlagReport({subject: subject, score: 1, ruleBitmask: 1}));
  }

  function test_scoreAbove100Reverts() public {
    vm.prank(forwarder);
    vm.expectRevert(abi.encodeWithSelector(FraudRegistry.InvalidScore.selector, 101));
    registry.onReport("", _report(subject, 101, 1));
  }

  function test_latestReportWins() public {
    vm.startPrank(forwarder);
    registry.onReport("", _report(subject, 30, 1));
    registry.onReport("", _report(subject, 90, 2));
    vm.stopPrank();
    assertEq(registry.getFlag(subject).score, 90);
    assertEq(registry.getFlag(subject).ruleBitmask, 2);
  }

  function test_supportsIReceiver() public view {
    assertTrue(registry.supportsInterface(type(IReceiver).interfaceId));
    assertTrue(registry.supportsInterface(type(IERC165).interfaceId));
  }

  function test_zeroForwarderRejectedAtDeploy() public {
    vm.expectRevert(ReceiverTemplate.InvalidForwarderAddress.selector);
    new FraudRegistry(address(0));
  }
}
