// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.36.6
// 	protoc        v5.29.3
// source: mock.proto

package mock

import (
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	emptypb "google.golang.org/protobuf/types/known/emptypb"
	reflect "reflect"
	sync "sync"
	unsafe "unsafe"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

type CleanUpPreimageShareRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	PaymentHash   []byte                 `protobuf:"bytes,1,opt,name=payment_hash,json=paymentHash,proto3" json:"payment_hash,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *CleanUpPreimageShareRequest) Reset() {
	*x = CleanUpPreimageShareRequest{}
	mi := &file_mock_proto_msgTypes[0]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *CleanUpPreimageShareRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*CleanUpPreimageShareRequest) ProtoMessage() {}

func (x *CleanUpPreimageShareRequest) ProtoReflect() protoreflect.Message {
	mi := &file_mock_proto_msgTypes[0]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use CleanUpPreimageShareRequest.ProtoReflect.Descriptor instead.
func (*CleanUpPreimageShareRequest) Descriptor() ([]byte, []int) {
	return file_mock_proto_rawDescGZIP(), []int{0}
}

func (x *CleanUpPreimageShareRequest) GetPaymentHash() []byte {
	if x != nil {
		return x.PaymentHash
	}
	return nil
}

var File_mock_proto protoreflect.FileDescriptor

const file_mock_proto_rawDesc = "" +
	"\n" +
	"\n" +
	"mock.proto\x12\x04mock\x1a\x1bgoogle/protobuf/empty.proto\"@\n" +
	"\x1bCleanUpPreimageShareRequest\x12!\n" +
	"\fpayment_hash\x18\x01 \x01(\fR\vpaymentHash2e\n" +
	"\vMockService\x12V\n" +
	"\x17clean_up_preimage_share\x12!.mock.CleanUpPreimageShareRequest\x1a\x16.google.protobuf.Empty\"\x00B+Z)github.com/lightsparkdev/spark/proto/mockb\x06proto3"

var (
	file_mock_proto_rawDescOnce sync.Once
	file_mock_proto_rawDescData []byte
)

func file_mock_proto_rawDescGZIP() []byte {
	file_mock_proto_rawDescOnce.Do(func() {
		file_mock_proto_rawDescData = protoimpl.X.CompressGZIP(unsafe.Slice(unsafe.StringData(file_mock_proto_rawDesc), len(file_mock_proto_rawDesc)))
	})
	return file_mock_proto_rawDescData
}

var file_mock_proto_msgTypes = make([]protoimpl.MessageInfo, 1)
var file_mock_proto_goTypes = []any{
	(*CleanUpPreimageShareRequest)(nil), // 0: mock.CleanUpPreimageShareRequest
	(*emptypb.Empty)(nil),               // 1: google.protobuf.Empty
}
var file_mock_proto_depIdxs = []int32{
	0, // 0: mock.MockService.clean_up_preimage_share:input_type -> mock.CleanUpPreimageShareRequest
	1, // 1: mock.MockService.clean_up_preimage_share:output_type -> google.protobuf.Empty
	1, // [1:2] is the sub-list for method output_type
	0, // [0:1] is the sub-list for method input_type
	0, // [0:0] is the sub-list for extension type_name
	0, // [0:0] is the sub-list for extension extendee
	0, // [0:0] is the sub-list for field type_name
}

func init() { file_mock_proto_init() }
func file_mock_proto_init() {
	if File_mock_proto != nil {
		return
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: unsafe.Slice(unsafe.StringData(file_mock_proto_rawDesc), len(file_mock_proto_rawDesc)),
			NumEnums:      0,
			NumMessages:   1,
			NumExtensions: 0,
			NumServices:   1,
		},
		GoTypes:           file_mock_proto_goTypes,
		DependencyIndexes: file_mock_proto_depIdxs,
		MessageInfos:      file_mock_proto_msgTypes,
	}.Build()
	File_mock_proto = out.File
	file_mock_proto_goTypes = nil
	file_mock_proto_depIdxs = nil
}
