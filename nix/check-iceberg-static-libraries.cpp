#include <aws/core/Aws.h>
#include <aws/core/AmazonWebServiceResult.h>
#include <aws/core/auth/AWSCredentialsProvider.h>
#include <aws/core/auth/signer/AWSAuthV4Signer.h>
#include <aws/core/client/ClientConfiguration.h>
#include <aws/core/http/HttpClientFactory.h>
#include <aws/core/http/HttpRequest.h>
#include <aws/core/utils/HashingUtils.h>
#include <aws/core/utils/json/JsonSerializer.h>
#include <aws/core/utils/stream/ResponseStream.h>
#include <aws/core/utils/xml/XmlSerializer.h>
#include <aws/sso/model/GetRoleCredentialsResult.h>
#include <aws/sts/model/AssumeRoleRequest.h>
#include <aws/sts/model/AssumeRoleResult.h>
#include <iostream>
#include <stdexcept>

static void require(bool condition, const char *message) {
  if (!condition) throw std::runtime_error(message);
}

static void check() {
  auto provider = Aws::MakeShared<Aws::Auth::SimpleAWSCredentialsProvider>(
      "receipt", "AKIDEXAMPLE", "receipt-only-secret", "receipt-only-token");
  Aws::Client::AWSAuthV4Signer signer(provider, "glue", "us-east-1");
  auto request = Aws::Http::CreateHttpRequest(
      Aws::String("https://catalog.invalid/v1/namespaces?prefix=demo"),
      Aws::Http::HttpMethod::HTTP_POST,
      Aws::Utils::Stream::DefaultResponseStreamFactoryMethod);
  auto body = Aws::MakeShared<Aws::StringStream>("receipt");
  *body << "abc";
  request->AddContentBody(body);
  request->SetContentLength("3");
  request->SetContentType("application/json");
  require(signer.SignRequest(*request), "SigV4 signing failed");
  const auto authorization = request->GetHeaderValue("authorization");
  const auto signature = authorization.find("Signature=");
  require(authorization.find("AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/") == 0 &&
              authorization.find("/us-east-1/glue/aws4_request") != Aws::String::npos &&
              signature != Aws::String::npos && authorization.size() - signature == 74,
          "SigV4 authorization scope or signature missing");
  require(request->GetHeaderValue("x-amz-security-token") == "receipt-only-token",
          "SigV4 session token missing");
  require(request->GetHeaderValue("x-amz-content-sha256") ==
              "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
          "SigV4 signed payload digest differs");
  Aws::Client::ClientConfiguration configuration;
  configuration.region = "us-east-1";
  require(Aws::Http::CreateHttpClient(configuration) != nullptr,
          "selected HTTP client unavailable");
  Aws::Utils::Json::JsonValue json(
      R"({"roleCredentials":{"accessKeyId":"fixture-access","secretAccessKey":"fixture-secret","sessionToken":"fixture-token","expiration":1893456000000}})");
  require(json.WasParseSuccessful(), "SSO JSON did not parse");
  Aws::SSO::Model::GetRoleCredentialsResult sso(
      Aws::AmazonWebServiceResult<Aws::Utils::Json::JsonValue>(std::move(json), {}));
  require(sso.GetRoleCredentials().GetAccessKeyId() == "fixture-access" &&
              sso.GetRoleCredentials().GetSessionToken() == "fixture-token" &&
              sso.GetRoleCredentials().GetExpiration() == 1893456000000LL,
          "SSO credential response differs");
  auto xml = Aws::Utils::Xml::XmlDocument::CreateFromXmlString(
      "<AssumeRoleResponse><AssumeRoleResult><Credentials>"
      "<AccessKeyId>fixture-access</AccessKeyId><SecretAccessKey>fixture-secret</SecretAccessKey>"
      "<SessionToken>fixture-token</SessionToken><Expiration>2030-01-01T00:00:00Z</Expiration>"
      "</Credentials></AssumeRoleResult></AssumeRoleResponse>");
  require(xml.WasParseSuccessful(), "STS XML did not parse");
  Aws::STS::Model::AssumeRoleResult sts(
      Aws::AmazonWebServiceResult<Aws::Utils::Xml::XmlDocument>(std::move(xml), {}));
  require(sts.GetCredentials().GetAccessKeyId() == "fixture-access" &&
              sts.GetCredentials().GetSessionToken() == "fixture-token" &&
              sts.GetCredentials().GetExpiration().Millis() == 1893456000000LL,
          "STS credential response differs");
  Aws::STS::Model::AssumeRoleRequest assume;
  assume.SetRoleArn("arn:aws:iam::123456789012:role/fixture");
  assume.SetRoleSessionName("receipt");
  require(assume.SerializePayload().find("Action=AssumeRole") != Aws::String::npos &&
              assume.SerializePayload().find("RoleSessionName=receipt") != Aws::String::npos,
          "STS request serialization differs");
}

int main() {
  Aws::SDKOptions options;
  Aws::InitAPI(options);
  int result = 0;
  try {
    check();
    std::cout << "selected static AWS SigV4 payload, HTTP client, SSO JSON and STS XML checks passed\n";
  } catch (const std::exception &error) {
    std::cerr << error.what() << '\n';
    result = 1;
  }
  Aws::ShutdownAPI(options);
  return result;
}
