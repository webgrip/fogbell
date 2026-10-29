'use strict';

const { makeConfig } = require('@webgrip/semantic-release-config');

module.exports = makeConfig({
  forge: 'github',
  branches: ['main', { name: 'development', prerelease: 'rc', channel: false }],
});
