const fs = require('fs');
const solc = require('solc');

const sourcePath = process.argv[2];
const outputDir = process.argv[3];
if (!sourcePath || !outputDir) {
  throw new Error('usage: node compile.js <source.sol> <out-dir>');
}

const input = {
  language: 'Solidity',
  sources: {
    'BatchWasmdQuery.sol': { content: fs.readFileSync(sourcePath, 'utf8') },
  },
  settings: {
    optimizer: { enabled: true, runs: 200 },
    evmVersion: 'paris',
    outputSelection: {
      '*': {
        '*': ['abi', 'evm.bytecode.object', 'evm.deployedBytecode.object'],
      },
    },
  },
};

const output = JSON.parse(solc.compile(JSON.stringify(input)));
const errors = output.errors || [];
for (const entry of errors) {
  console.error(entry.formattedMessage);
}
if (errors.some((entry) => entry.severity === 'error')) {
  process.exit(1);
}

const contract = output.contracts['BatchWasmdQuery.sol'].BatchWasmdQuery;
fs.mkdirSync(outputDir, { recursive: true });
fs.writeFileSync(`${outputDir}/BatchWasmdQuery.abi.json`, JSON.stringify(contract.abi, null, 2));
fs.writeFileSync(`${outputDir}/BatchWasmdQuery.bin`, `${contract.evm.bytecode.object}\n`);
fs.writeFileSync(`${outputDir}/BatchWasmdQuery.runtime.bin`, `${contract.evm.deployedBytecode.object}\n`);
console.log(JSON.stringify({
  solc: solc.version(),
  creationBytes: contract.evm.bytecode.object.length / 2,
  runtimeBytes: contract.evm.deployedBytecode.object.length / 2,
}, null, 2));
