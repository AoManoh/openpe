import { enhanceViaCli } from "./cli";
import { enhanceViaHttp } from "./http";
import { OpenPEExtensionConfig, OpenPERequest, OpenPEResponse } from "../core/types";

export async function enhancePrompt(
  request: OpenPERequest,
  config: OpenPEExtensionConfig
): Promise<OpenPEResponse> {
  if (config.transport === "http") {
    return enhanceViaHttp(request, config);
  }
  return enhanceViaCli(request, config);
}

