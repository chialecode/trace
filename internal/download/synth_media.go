package download

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"

	"github.com/Eyevinn/mp4ff/aac"
	"github.com/Eyevinn/mp4ff/mp4"
)

// Real 640x360 H.264 High Profile SPS, PPS, and non-black IDR I-slice NALU bytes.
// Decodable by Windows Media Foundation (H.264 MFT) into real 640x360 non-black RGBA video frames.
const (
	synthAVCSPSHex = "6764001eacd940a02ff9610000030001000003003c8f162d96"
	synthAVCPPSHex = "68ebecb22c"
	synthAVCIDRHex = "658884004ffede23c0a6109524aac69445999a203b80a0abf4f9b5ada1900db461783fcd085d5382bbebbc2bf405d2345c75285484c977589c165dcf43ac344de322f249ab0d74572b671ab2e088e7bd0221bf95f69b60f4d4ba11cab0140e0ada842384833066c9a9f1f273dc97ca1c1029f3ce19c338e0838670ce361ce19c338d3419c33869401c338671902be045ca9a800360ce507494da5ef3a6635afbf72fec163e67e6eb55e55f6ac99f19f72cd6fe167fc4c3dae4ca14386aad59e5c8b35cd936b4ccf90a1def866fa67b2a2c97fa5cecb27efa7b199b6b248defbef25304027aca0883d5c70fb97fbc4afc87dcdc41dd7702466e9ce9b3086b2df5cf74a5e0eea09549682ffe2ad46b53ce08126a7ede97eed7868528a0d3f3ffd8e3e34769da3ebc8be6f96cb1ad8259144c93fd2b5aa982b74b95d93c57a2e6c5d35b0872457e110ecd1f834bc24952f28bdf4537b1f9982159a6f04c4619b3a0c2d7b805f00bb7ba76ea20cd2e23f277ba6bbd67d7103ac2f721633266040255bcc5202640de072b3520de17b2d2a457dd9a55d3a2f5fe6acbbe576ad28d211088919dc77adfcbd3a0fcd47af7e1972e4530ae178b1f5e7dda8b049371f3ef02c670f80e3ebe9d626a8f8e8838ab2bc72254875f677d4a80482d4a805ac97d7646cf3a047ad5d209a437e3fc8f7c8bc7f4a8a13a0b6ef53b189536713d5dd195f280ee953fb9a989e57d6f3b8bf28caadc0af72c0da6597ea4db29e72223c93a873d3062c077795b88d6797e555e34517889aacdc45a411608afb817870fd66f490b8cb93474672f740c103b1c284822c6f92133180687464285faa9ea056a7ff60c63901df6ce690191dff0b225edd41be794436da718ac9ee13872b47f8072b3bc01da7cb673e4a3016eea12e15971e5ffa339f2fa1bc7e221fa0b090c38eae5b582a226b5b0e7d83519a42e0e9fbd0f642285598e1d5b8e5dfeff30d00b2df1eec25232c6252e9a3e154e484f8b4b54db1694a38c03af342247d0a4663c8f7e86beb6c4a287e1e5b5e114e7fe3a9209ea1847dd93e48f4b47ee6e7dbeaa5651c237430f9c332926e74a7cd3b47fc024ca7419ae116bf9cb2fff64e465ee4bfa820b6ce3eb31cd98b3a2db84416dadc22417b8b950fd8ec4e89f741dc53cccb10b36346d0408097b9da3e3f54d0b29ab46dc555eae5d24f6587a15c1ec80fc1370b20a0575ed4d597a6e88b3142455d20eb31e3d146d43c374acf2cc3e83e9a91d1f2ce4dad42257c217c3b9db1963a056c6e8b3f39efa1342b9492c22e653cade71ed8271fa90e63964fad40da7d7c0acc29ab193e928812eefeb497d53b0f8230fa25c0c2772a55e72bde334c330368df3c0e1d58947de797d9fd29a58713613c80f7474696adc80f7e1fa099760044875d15c7be296bff49658abaff05a0c10e31abd6b695c2ed63dee603474e1304965bd2df9feeea03493f778af3be93f3f153f3384fecb746c3953d93cec8dce93bface5bb4376c2df1f22361fdeb8e82851af05722c18b95218b85576e44c138b684550cb097d756e963c5b72db7346ad2c9040f3c3bb424e05a7b79df102c330da349f4bcd91b17ada4c3be8f5af0f04584578bd1e3aa6cd6a28f993686c4ffa0467cf6c027d78936ecdbe8207bb3c787a5f773bd154a53e09be47ba52f002e49c77bcafbf8f05e782a02313423168c02a6cb852027253dbb0dfb9fea44ac8e416a61f062b6bebd46d5ce8f258e91f562f9b54fefdae6ee776d47f04eded5c56bb63e7551c330d1c9b34cbfb2bd420b2be853163a5db4437af40e26c0f2a83c1c16cdbe4d65eb5bb4abb195641ced78aadb756bb18a36536f05e924d6dd301d7c8e64bf25809924d318c5913100a2f6fb66ffedfe33a2f3c4ad1a336d2bf0ad225259049810319a5f5ee536283eb94f2ea1f654eb0f01baf570386b0f9f4acfd20802ece7656e264fea2573b2457229ed2579e5c230354f251ea1ee311f49c38c305ac45ad4716a43ae0c38cbeaaa9acc28712e6b218f0c9bfa35000ebb6d11e923f62855000e4472898b08c8510b2ebe6e01bf7e7afe880d82d6b8a9c536caca35970f33572123e420a40debb31c8c46a990b81a93761c138a7f6b4b2e3f86fef23f270240ec3d0838cc2ac9edb57658afcf2c2b3f190e8d8c646e9e48c5dba907cb70086eec3422df7817e204f81f1d38923e2678791b32ce307d8054faae5a1007669a4651e9b01dd3048a5c610b25c074b9797893804830e172a8bea4b21b58e14a390b449930773c12fb2037449a5e8d272e0628a065433d41015a1f265af493e05a3b08e26fb91dc49868a55a81abc92021f16198f2279d629d33437fa0167ba54cf35087fb6ac05836147ece8518341969d64af75b57434eacd0ddee68c41acc1d3317896b16a514b0d335594414195c9303af1d861d82f79c0b27c4d276dec8c48113c6f516a1051cc459ff5c195eed6189101ef14a76d3a50fb7dd7633505e1eb73caad22a0458ddaeb4f85721b2f92962cdf6b44090234711c14686ecccad44c1c0afb58737c3754d2ce3a4c80ccd766d00174c863758d639e3b3e6f7f1deb43774957b81b910891b179de5d535f71f0fae0ecc2d61b0006d10738999dd0ddc7a5384814bd614555112fd60dd72979e8ff9a951907023d86969805d2572e4fb1ede661a49168f0e3d50760c7b5b2bc3a1ca43cc8294afed7b6f6deba3b4084fffc3edcce5c888df4ee2c645cdcb9245d92f44666d269fda2e685522ef74dbb1ef8cfe6efec7a0ca0d7bf9fedb3031eff06ebc8546546c2785696188466747e0c2b5f4edf7a08250b76a36e574a1b85fcc63287f64cede8f21dd8803e6e524f13e3eda55abcbe7e0590b3fffff284bcd2f8ff662d7a0aa9f5a5d789b507fe7c234059ee71e9aa3d94ec8e3d5de6422ea36828c7aaf0d5d0e9931badd7ae69bf5a3acf11f98267f6b4ec1611c87c2956d2fba8157717be0c780000472f17a9204239c47b0ec7c428deb31ea5f81ea621f6aaa665be5b37bac9bea5cdac01eae8b65435519f744685a9581944c26ca37dfe6b9baac28b8abae28f73b87142d9ac5cc4f71f2a8ff7c9b1a0b51ecaa6d973ba9fcbceae37bee663074c6e73ea9fec55f61e476f884af801fc34bb448e46f43eb3a5e951269de1eb6a7085becef8334602413c80098b2e7cd4db92bfd7029e70b1c9d49660710d3ac2eae8743d7795ad30af83a0fc7ae10cfa04a5d6bdb4da40dae1d780daab56d3aa6fd9d7eb29ed83914a41"
)

// BuildSyntheticVideoM4S generates a valid DASH video .m4s (ftyp + moov + moof + mdat)
// with H.264 (avc1) samples for deterministic offline tests, prototypes, and smoke runs.
func BuildSyntheticVideoM4S(frameCount int, fps int) ([]byte, error) {
	if frameCount <= 0 {
		frameCount = 30
	}
	if fps <= 0 {
		fps = 30
	}
	sps, _ := hex.DecodeString(synthAVCSPSHex)
	pps, _ := hex.DecodeString(synthAVCPPSHex)
	idr, _ := hex.DecodeString(synthAVCIDRHex)

	// Length-prefixed (4-byte big-endian) NALU sample payload for avc1 (SPS + PPS + IDR).
	sampleData := make([]byte, 4+len(sps)+4+len(pps)+4+len(idr))
	off := 0
	for _, nalu := range [][]byte{sps, pps, idr} {
		binary.BigEndian.PutUint32(sampleData[off:off+4], uint32(len(nalu)))
		copy(sampleData[off+4:off+4+len(nalu)], nalu)
		off += 4 + len(nalu)
	}

	timescale := uint32(fps * 1000)
	sampleDur := uint32(1000)

	init := mp4.CreateEmptyInit()
	init.Moov.Mvhd.Timescale = timescale
	vTrak := init.AddEmptyTrack(timescale, "video", "und")
	if err := vTrak.SetAVCDescriptor("avc1", [][]byte{sps}, [][]byte{pps}, true); err != nil {
		return nil, err
	}
	if vTrak.Tkhd != nil {
		vTrak.Tkhd.Width = mp4.Fixed32(640 << 16)
		vTrak.Tkhd.Height = mp4.Fixed32(360 << 16)
	}

	var buf bytes.Buffer
	if err := init.Encode(&buf); err != nil {
		return nil, err
	}

	frag, err := mp4.CreateFragment(1, 1)
	if err != nil {
		return nil, err
	}
	syncFlags := mp4.SampleFlags{SampleDependsOn: 2}.Encode()
	for i := 0; i < frameCount; i++ {
		frag.AddFullSample(mp4.FullSample{
			Sample: mp4.Sample{
				Flags:                 syncFlags,
				Dur:                   sampleDur,
				Size:                  uint32(len(sampleData)),
				CompositionTimeOffset: 0,
			},
			DecodeTime: uint64(i) * uint64(sampleDur),
			Data:       sampleData,
		})
	}
	if err := frag.Encode(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// BuildSyntheticAudioM4S generates a valid DASH audio .m4s (ftyp + moov + moof + mdat)
// with AAC-LC 48kHz stereo silent frames.
func BuildSyntheticAudioM4S(frameCount int) ([]byte, error) {
	if frameCount <= 0 {
		frameCount = 45
	}
	timescale := uint32(48000)
	sampleDur := uint32(1024)
	// Valid 6-byte raw AAC-LC stereo CPE silent frame payload.
	aacSilentFrame := []byte{0x21, 0x10, 0x04, 0x60, 0x8c, 0x1c}

	init := mp4.CreateEmptyInit()
	init.Moov.Mvhd.Timescale = timescale
	aTrak := init.AddEmptyTrack(timescale, "audio", "und")
	if err := aTrak.SetAACDescriptor(aac.AAClc, int(timescale)); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := init.Encode(&buf); err != nil {
		return nil, err
	}

	frag, err := mp4.CreateFragment(1, 1)
	if err != nil {
		return nil, err
	}
	syncFlags := mp4.SampleFlags{SampleDependsOn: 2}.Encode()
	for i := 0; i < frameCount; i++ {
		frag.AddFullSample(mp4.FullSample{
			Sample: mp4.Sample{
				Flags: syncFlags,
				Dur:   sampleDur,
				Size:  uint32(len(aacSilentFrame)),
			},
			DecodeTime: uint64(i) * uint64(sampleDur),
			Data:       aacSilentFrame,
		})
	}
	if err := frag.Encode(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
