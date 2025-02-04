import { Link } from "../components/link";
import { Hint } from "../components/hint";
import { Warp } from "../components/warp";
import {
  ResourceTransformationArgs,
  interpolate,
  mergeOptions,
  runtime,
  automation,
} from "@pulumi/pulumi";

export async function run(program: automation.PulumiFn) {
  process.chdir($cli.paths.root);

  runtime.registerStackTransformation((args: ResourceTransformationArgs) => {
    if (
      $app.removalPolicy === "retain-all" ||
      ($app.removalPolicy === "retain" &&
        [
          "aws:s3/bucket:Bucket",
          "aws:s3/bucketV2:BucketV2",
          "aws:dynamodb/table:Table",
        ].includes(args.type))
    ) {
      return {
        props: args.props,
        opts: mergeOptions({ retainOnDelete: true }, args.opts),
      };
    }
    return undefined;
  });

  runtime.registerStackTransformation((args: ResourceTransformationArgs) => {
    let normalizedName = args.name;
    if (
      args.type === "pulumi-nodejs:dynamic:Resource" ||
      args.type === "pulumi:providers:aws"
    ) {
      const parts = args.name.split(".");
      if (parts.length === 3 && parts[1] === "sst") {
        normalizedName = parts[0];
      }
    }

    if (!normalizedName.match(/^[A-Z][a-zA-Z0-9]*$/)) {
      throw new Error(
        `Invalid component name "${normalizedName}". Component names must start with an uppercase letter and contain only alphanumeric characters.`
      );
    }

    return undefined;
  });

  Link.makeLinkable(aws.dynamodb.Table, function () {
    return {
      type: `{ tableName: string }`,
      value: { tableName: this.name },
    };
  });
  Link.AWS.makeLinkable(aws.dynamodb.Table, function () {
    return {
      actions: ["dynamodb:*"],
      resources: [this.arn, interpolate`${this.arn}/*`],
    };
  });

  Hint.reset();
  Link.reset();
  Warp.reset();
  const outputs = (await program()) || {};
  outputs._links = Link.list();
  outputs._hints = Hint.list();
  outputs._warps = Warp.list();
  outputs._receivers = Link.Receiver.list();
  return outputs;
}
