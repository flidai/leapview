# Every SSHKit command and upload runs under the host supervisor, including
# Kamal's activation-lock acquisition/release. Never bypass it on failure.
require 'sshkit'
require 'sshkit/backends/netssh'
require 'base64'
require 'shellwords'

module LeapViewSupervisedSSH
  def execute_command(command)
    original = command.to_command
    raise 'registry login is forbidden in the public-pull operator' if original.match?(/\bdocker\s+login\b/)
    source = Base64.strict_decode64(ENV.fetch('SITE_SUPERVISOR_SOURCE'))
    guarded = ['python3', '-c', source, 'exec', ENV.fetch('SITE_ATTEMPT'), original].shelljoin
    command.define_singleton_method(:to_command) { guarded }
    super(command)
  end

  def upload!(local, remote, options = {})
    raise 'recursive uploads are not qualified' if options[:recursive]
    data = local.respond_to?(:read) ? local.read : File.binread(local)
    remote = File.join(pwd_path, remote) unless remote.to_s.start_with?('/') || pwd_path.nil?
    source = "import base64,pathlib; p=pathlib.Path(#{remote.to_s.inspect}); p.write_bytes(base64.b64decode(#{Base64.strict_encode64(data).inspect}))"
    execute :python3, '-c', Shellwords.escape(source)
  end
end
SSHKit::Backend::Netssh.prepend(LeapViewSupervisedSSH)
