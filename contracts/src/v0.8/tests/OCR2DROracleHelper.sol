// SPDX-License-Identifier: MIT
pragma solidity ^0.8.6;

import "../dev/ocr2dr/OCR2DROracle.sol";

contract OCR2DROracleHelper is OCR2DROracle {
  function callValidateReport(bytes calldata report) external pure returns (bool isValid) {
    bytes32 configDigest;
    uint40 epochAndRound;
    isValid = _validateReport(configDigest, epochAndRound, report);
  }

  function callReport(bytes calldata report) external {
    uint32 initialGas;
    address transmitter;
    address[maxNumOracles] memory signers;
    _report(initialGas, transmitter, signers, report);
  }
}
