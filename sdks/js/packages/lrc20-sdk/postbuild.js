import fs from "fs";
import { replaceTscAliasPaths } from "tsc-alias";
import path from 'path';

const writeFile = (path, data) => {
  fs.writeFile(path, JSON.stringify(data), (err) => {
    if (err) throw err;
  });
};

const postBuild = (dirpath) => {
  writeFile(`${dirpath}/dist/esm/package.json`, {
    type: "module",
  });
  writeFile(`${dirpath}/dist/cjs/package.json`, {
    type: "commonjs",
  });

  replaceTscAliasPaths({
    tsconfigPath: `${dirpath}/tsconfig.json`,
    outDir: `${dirpath}/dist/types`,
  });
};
postBuild(path.resolve());
