// @ts-ignore
/// <reference path="./.sst/platform/src/global.d.ts" />

export default $config({
  app(input) {
    return {
      name: "{{.App}}",
      removalPolicy: input?.stage === "production" ? "retain" : "remove",
    };
  },
  async run() {},
});
