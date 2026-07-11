#!/usr/bin/env node
'use strict'; const { spawnSync }=require('node:child_process'); const path=require('node:path'); const fs=require('node:fs');
const command=path.basename(process.argv[1],'.js'); const pkg=`@nebutra/carina-${process.platform}-${process.arch}`; let root
try{root=path.dirname(require.resolve(`${pkg}/package.json`))}catch(e){console.error(`Carina has no native package for ${process.platform}-${process.arch}. Reinstall with optional dependencies enabled.`);process.exit(127)}
const binary=path.join(root,'bin',command); if(!fs.existsSync(binary)){console.error(`Carina native package is incomplete: ${binary}`);process.exit(127)}
const result=spawnSync(binary,process.argv.slice(2),{stdio:'inherit',env:process.env});if(result.error){console.error(result.error.message);process.exit(127)}process.exit(result.status??1)
