// Exercise selected SDK archives without network access or real credentials.
#include <azure/core/http/transport.hpp>
#include <azure/core/io/body_stream.hpp>
#include <azure/identity/client_secret_credential.hpp>
#include <azure/storage/blobs/blob_service_client.hpp>
#include <azure/storage/files/datalake/datalake_service_client.hpp>
#include <iostream>
#include <stdexcept>
#include <vector>

class FixtureTransport final : public Azure::Core::Http::HttpTransport {
  std::vector<uint8_t> body;
public:
  unsigned signedRequests = 0;
  unsigned tokenRequests = 0;
  std::unique_ptr<Azure::Core::Http::RawResponse> Send(
      Azure::Core::Http::Request &request, Azure::Core::Context const &) override {
    const auto url = request.GetUrl().GetAbsoluteUrl();
    std::string payload;
    if (url.find("login.microsoftonline.com/") != std::string::npos) {
      ++tokenRequests;
      payload = R"({"access_token":"selected-native-token","expires_in":3600,"token_type":"Bearer"})";
    } else {
      const auto headers = request.GetHeaders();
      const auto authorization = headers.find("Authorization");
      if (authorization == headers.end() ||
          authorization->second.find("SharedKey fixture:") != 0)
        throw std::runtime_error("selected storage client did not sign its request");
      ++signedRequests;
      payload = R"(<EnumerationResults ServiceEndpoint="https://fixture.blob.core.windows.net/"><Containers><Container><Name>retained</Name><Properties><Last-Modified>Wed, 01 Jan 2025 00:00:00 GMT</Last-Modified><Etag>&quot;one&quot;</Etag><LeaseStatus>unlocked</LeaseStatus><LeaseState>available</LeaseState><HasImmutabilityPolicy>false</HasImmutabilityPolicy><HasLegalHold>false</HasLegalHold></Properties></Container></Containers><NextMarker /></EnumerationResults>)";
    }
    body.assign(payload.begin(), payload.end());
    auto response = std::make_unique<Azure::Core::Http::RawResponse>(
        1, 1, Azure::Core::Http::HttpStatusCode::Ok, "OK");
    response->SetHeader("Content-Length", std::to_string(body.size()));
    response->SetHeader("x-ms-request-id", "selected-native-fixture");
    response->SetBodyStream(std::make_unique<Azure::Core::IO::MemoryBodyStream>(body));
    return response;
  }
};

int main() {
  try {
    auto transport = std::make_shared<FixtureTransport>();
    auto key = std::make_shared<Azure::Storage::StorageSharedKeyCredential>("fixture", "c2VsZWN0ZWQtbmF0aXZlLWZpeHR1cmU=");
    Azure::Storage::Blobs::BlobClientOptions blobOptions;
    blobOptions.Transport.Transport = transport;
    Azure::Storage::Blobs::BlobServiceClient blobs("https://fixture.blob.core.windows.net/", key, blobOptions);
    auto containers = blobs.ListBlobContainers();
    if (containers.BlobContainers.size() != 1 || containers.BlobContainers[0].Name != "retained")
      throw std::runtime_error("selected blob XML roundtrip failed");
    Azure::Storage::Files::DataLake::DataLakeClientOptions lakeOptions;
    lakeOptions.Transport.Transport = transport;
    Azure::Storage::Files::DataLake::DataLakeServiceClient lake("https://fixture.dfs.core.windows.net/", key, lakeOptions);
    auto systems = lake.ListFileSystems();
    if (systems.FileSystems.size() != 1 || systems.FileSystems[0].Name != "retained")
      throw std::runtime_error("selected datalake XML roundtrip failed");
    Azure::Identity::ClientSecretCredentialOptions identityOptions;
    identityOptions.Transport.Transport = transport;
    Azure::Identity::ClientSecretCredential identity("fixture-tenant", "fixture-client", "owned-fixture-secret", identityOptions);
    Azure::Core::Credentials::TokenRequestContext tokenContext;
    tokenContext.Scopes = {"https://storage.azure.com/.default"};
    if (identity.GetToken(tokenContext, Azure::Core::Context()).Token != "selected-native-token" ||
        transport->signedRequests != 2 || transport->tokenRequests != 1)
      throw std::runtime_error("selected identity roundtrip failed");
    std::cout << "selected static Azure SDK signed blob/datalake XML and identity roundtrips passed\n";
  } catch (const std::exception &error) {
    std::cerr << error.what() << '\n';
    return 1;
  }
}
