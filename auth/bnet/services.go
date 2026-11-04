package bnet

// Service is one bgs service as the client addresses it.
//
// Outbound requests are addressed by hash. The generated stubs pass
// (hash, method) to the connection manager (e.g. 0x1023948c0 calls
// vtable+0x18 with 0x5dbb51c2, 1, ...) and register each method under the
// 64-bit key hash<<32 | flags | method (FUN_1022e2ddc in the registrars
// 0x102393bfc and 0x10239446c), so the hash really is the routing identity.
//
// The hash is FNV-1a (32-bit) of the service's descriptor_name, the string in
// BGSServiceOptions (extension 90000, field 1) on the service in the embedded
// descriptor. TestServiceHashes recomputes every constant below from its name,
// and each literal also occurs as an immediate in the client's code at the
// address given beside it, which is independent evidence that FNV-1a over that
// name is what the client uses.
type Service struct {
	// Name is BGSServiceOptions.descriptor_name.
	Name string
	// Proto is the protobuf package-qualified service name.
	Proto string
	// Hash is FNV-1a-32(Name).
	Hash uint32
	// UseClientID is SDKServiceOptions.use_client_id: the client attaches its
	// client instance id (Header.ciid) to requests for this service. In the
	// registrar this shows up as the 0x40000000 flag in the method key
	// (0x5dbb51c240000001 for ProcessTask, against 0x6544699100000001 for
	// Connect).
	UseClientID bool
}

// The services present in the image. Method ids are BGSMethodOptions.id from
// the descriptors (tools/pbdesc.py dump <file>).
var (
	// ConnectionService: bgs/low/pb/client/connection_service.proto. Hash
	// immediate at 0x10238f4bc and in the Connect stub 0x1023942f8.
	ConnectionService = Service{
		Name:  "bnet.protocol.connection.ConnectionService",
		Proto: "bgs.protocol.connection.v1.ConnectionService",
		Hash:  0x65446991,
	}
	// AuthenticationService: authentication_service.proto (v2). Hash immediate
	// at 0x1023928c4 / Logon stub 0x102392dd0.
	AuthenticationService = Service{
		Name:        "bnet.protocol.authentication.v2.client.AuthenticationService",
		Proto:       "bgs.protocol.authentication.v2.client.AuthenticationService",
		Hash:        0xc02f8216,
		UseClientID: true,
	}
	// AuthenticationListener: the service the server calls back on
	// (authentication_listener.proto). Hash immediate at 0x10238e6a0 and as
	// fixed32 data at image offset 0x31f79e8.
	AuthenticationListener = Service{
		Name:  "bnet.protocol.authentication.v2.client.AuthenticationListener",
		Proto: "bgs.protocol.authentication.v2.client.AuthenticationListener",
		Hash:  0x9da8116b,
		// Inbound; its service options carry use_client_id as well.
		UseClientID: true,
	}
	// GameUtilities: game_utilities_service.proto (v2). Hash immediate at
	// 0x102394498, registrar 0x10239446c.
	GameUtilities = Service{
		Name:        "bnet.protocol.game_utilities.v2.client.GameUtilities",
		Proto:       "bgs.protocol.game_utilities.v2.client.GameUtilitiesService",
		Hash:        0x5dbb51c2,
		UseClientID: true,
	}
	// AccountService: account_service.proto (v2). Hash immediate at
	// 0x10238fcd8.
	AccountService = Service{
		Name:        "bnet.protocol.account.v2.client.AccountService",
		Proto:       "bgs.protocol.account.v2.client.AccountService",
		Hash:        0x22dc2464,
		UseClientID: true,
	}
)

// Services lists every service this package knows.
var Services = []Service{
	ConnectionService, AuthenticationService, AuthenticationListener, GameUtilities, AccountService,
}

// ServiceHash is the hash the client derives from a descriptor name.
func ServiceHash(descriptorName string) uint32 {
	h := uint32(0x811c9dc5)
	for i := 0; i < len(descriptorName); i++ {
		h = (h ^ uint32(descriptorName[i])) * 0x01000193
	}
	return h
}

// Method ids (BGSMethodOptions.id). The service options of the connection
// service mark Bind and Encrypt as `obsolete` (method option field 10), which
// is why the client connects with use_bindless_rpc and never calls Bind.
const (
	MethodConnect           = 1 // ConnectionService.Connect
	MethodBind              = 2 // ConnectionService.Bind, obsolete
	MethodEcho              = 3
	MethodForceDisconnect   = 4
	MethodKeepAlive         = 5
	MethodEncrypt           = 6 // obsolete
	MethodRequestDisconnect = 7

	MethodLogon             = 1 // AuthenticationService.Logon
	MethodVerifyAuthToken   = 2
	MethodGenerateAuthToken = 3

	MethodOnLogonComplete     = 1 // AuthenticationListener.*, server to client
	MethodOnLogonQueueUpdate  = 2
	MethodOnLogonQueueEnd     = 3
	MethodOnExternalChallenge = 4

	MethodProcessTask               = 1 // GameUtilities.ProcessTask
	MethodGetAllValuesForAttributes = 2 // GameUtilities.GetAllValuesForAttribute
)

// Router maps service hashes to the numeric service ids a bound (non-bindless)
// connection uses in Header.service_id. In bindless mode, which is the only
// mode the shipped client uses, every id is 0 and the hash alone routes.
type Router struct {
	idByHash map[uint32]uint32
	hashByID map[uint32]uint32
}

// NewRouter returns an empty (bindless) routing table.
func NewRouter() *Router {
	return &Router{idByHash: map[uint32]uint32{}, hashByID: map[uint32]uint32{}}
}

// Set binds hash to id.
func (r *Router) Set(hash, id uint32) {
	r.idByHash[hash] = id
	r.hashByID[id] = hash
}

// ServiceID returns the id bound to hash, or 0 (bindless) if none is.
func (r *Router) ServiceID(hash uint32) uint32 { return r.idByHash[hash] }

// Hash resolves an inbound request to its service: the explicit hash if the
// header carries one, otherwise the hash bound to the header's service id.
func (r *Router) Hash(h Header) (uint32, bool) {
	if h.ServiceHash != 0 {
		return h.ServiceHash, true
	}
	hash, ok := r.hashByID[h.ServiceID]
	return hash, ok
}
