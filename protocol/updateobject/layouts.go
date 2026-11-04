package updateobject

// Create layouts of the update-field structs, in the client's read order.
//
// Each was checked by running the client's own reader in an emulator
// (tools/ufemu.py) over bytes this package encodes, for every visibility
// flag set and with non-zero counts and set presence bits; the recorded runs
// are in testdata/ufemu/ and replayed by TestLayoutsAgainstClient. The
// in-memory offset each field is stored at is given as +0x…, so a field can
// be named from research/update_fields.json (which keys names by reader
// address) or, where the fork's order and the client's disagree, left as
// Unk_<offset>. Names follow the TrinityCore "forever" fork's UpdateFields.h.

// ObjectData, reader 0x1008ce2b8.
var objectDataCreate = ops(
	i32("EntryID"),      // +0x0
	u32("DynamicFlags"), // +0x4
	f32("Scale"),        // +0x8
)

// visibleItem is VisibleItem (client reader 0x1008e2858), used by UnitData
// (3 virtual items) and PlayerData (19 visible items).
var visibleItem = ops(
	i32("ItemID"),                            // +0x0
	i32("SecondaryItemModifiedAppearanceID"), // +0x4
	i32("ConditionalItemAppearanceID"),       // +0x8
	i32("Unk_0xC"),                           // +0xC
	u16("ItemAppearanceModID"),               // +0x10
	u16("ItemVisual"),                        // +0x12
	u32("ItemModifiedAppearanceID"),          // +0x18
	u8("TransmogSlotOption"),                 // +0x1C
	u8("SheatheCategory"),                    // +0x1D
	bit("HasTransmog"),                       // one byte at +0x14
	bit("HasIllusion"),
)

// UnitData, reader 0x1008e3fa0.
var unitDataCreate = ops(
	i32("DisplayID"),          // +0x0
	u32("NpcFlags"),           // +0x4
	u32("NpcFlags2"),          // +0x8
	u32("StateSpellVisualID"), // +0xC
	u32("StateAnimID"),        // +0x10
	u32("StateAnimKitID"),     // +0x14
	u32("StateWorldEffectIDs.size"),
	u32("StateWorldEffectsQuestObjectiveID"), // +0x30
	i32("SpellOverrideNameID"),               // +0x34
	each("StateWorldEffectIDs.size", u32("StateWorldEffectIDs")),
	guid("Charm"),  // +0x38
	guid("Summon"), // +0x48
	when(FlagOwner, guid("Critter")),
	guid("CharmedBy"),              // +0x68
	guid("SummonedBy"),             // +0x78
	guid("CreatedBy"),              // +0x88
	guid("DemonCreator"),           // +0x98
	guid("LookAtControllerTarget"), // +0xA8
	guid("Target"),                 // +0xB8
	guid("BattlePetCompanionGUID"), // +0xC8
	u64("BattlePetDBID"),           // +0xD8
	guid("BattlePetAttachedToDecorGUID"),
	guid("BattlePetDecorHouseGUID"),
	i32("ChannelData.SpellID"),                         // +0x100
	i32("ChannelData.SpellVisual.SpellXSpellVisualID"), // +0x104
	i32("ChannelData.SpellVisual.ScriptVisualID"),      // +0x108
	u32("ChannelData.StartTimeMs"),                     // +0x10C
	u32("ChannelData.Duration"),                        // +0x110
	i8("SpellEmpowerStage"),                            // +0x114
	u32("SummonedByHomeRealm"),                         // +0x118
	u8("Race"),                                         // +0x11C
	u8("ClassId"),                                      // +0x11D
	u8("PlayerClassId"),                                // +0x11E
	u8("Sex"),                                          // +0x11F
	u8("CreatureType"),                                 // +0x120
	u8("DisplayPower"),                                 // +0x121
	u32("OverrideDisplayPowerID"),                      // +0x124
	i64("Health"),                                      // +0x128
	repeat(10, i32("Power"), i32("MaxPower")),          // +0x3C8, +0x3F0
	when(FlagOwner|FlagUnitAll, repeat(10,
		f32("PowerRegenFlatModifier"),            // +0x418
		f32("PowerRegenInterruptedFlatModifier"), // +0x440
	)),
	i64("MaxHealth"),          // +0x130
	i32("Level"),              // +0x138
	i32("EffectiveLevel"),     // +0x13C
	i32("ContentTuningID"),    // +0x140
	i32("ScalingLevelMin"),    // +0x144
	i32("ScalingLevelMax"),    // +0x148
	i32("ScalingLevelDelta"),  // +0x14C
	u8("ScalingFactionGroup"), // +0x150
	i32("FactionTemplate"),    // +0x154
	repeat(3, prefixed("VirtualItems.", visibleItem)...), // +0x468, 0x20 each
	u32("Flags"),                  // +0x158
	u32("Flags2"),                 // +0x15C
	u32("Flags3"),                 // +0x160
	u32("Flags4"),                 // +0x164
	u32("AuraState"),              // +0x168
	u32("AttackRoundBaseTime[0]"), // +0x4C8
	u32("AttackRoundBaseTime[1]"), // +0x4CC
	when(FlagOwner, u32("RangedAttackRoundBaseTime")), // +0x16C
	f32("BoundingRadius"),                             // +0x170
	f32("CombatReach"),                                // +0x174
	f32("DisplayScale"),                               // +0x178
	i32("CreatureFamily"),                             // +0x17C
	u8("OverrideCreatureType"),                        // +0x180
	i32("NativeDisplayID"),                            // +0x184
	f32("NativeXDisplayScale"),                        // +0x188
	i32("MountDisplayID"),                             // +0x18C
	i32("CosmeticMountDisplayID"),                     // +0x190
	when(FlagOwner|FlagEmpath,
		f32("MinDamage"),        // +0x194
		f32("MaxDamage"),        // +0x198
		f32("MinOffHandDamage"), // +0x19C
		f32("MaxOffHandDamage"), // +0x1A0
	),
	u8("StandState"),              // +0x1A4
	u8("PetTalentPoints"),         // +0x1A5
	u8("AnimTier"),                // +0x1A6
	u8("VisFlags"),                // +0x1A7
	u8("Unk_0x1A8"),               // +0x1A8
	u32("PetNumber"),              // +0x1AC
	u32("PetNameTimestamp"),       // +0x1B0
	u32("PetExperience"),          // +0x1B4
	u32("PetNextLevelExperience"), // +0x1B8
	f32("ModCastingSpeed"),        // +0x1BC
	f32("ModCastingSpeedNeg"),     // +0x1C0
	f32("ModHaste"),               // +0x1C4
	// The client reads the owner-only float fourth; the fork writes
	// ModSpellHaste third, which swaps it with ModHaste for the owner.
	when(FlagOwner, f32("ModSpellHaste")), // +0x1C8
	f32("ModRangedHaste"),                 // +0x1CC
	f32("ModHasteRegen"),                  // +0x1D0
	f32("ModTimeRate"),                    // +0x1D4
	i32("CreatedBySpell"),                 // +0x1D8
	i32("EmoteState"),                     // +0x1DC
	i16("ClassicTrainingPointsUsed"),      // +0x1E0
	i16("ClassicTrainingPointsTotal"),     // +0x1E2
	when(FlagOwner, repeat(5,
		i32("Stats"),           // +0x4D0
		i32("StatPosBuff"),     // +0x4E4
		i32("StatNegBuff"),     // +0x4F8
		i32("StatSupportBuff"), // +0x50C
	)),
	when(FlagOwner|FlagEmpath, repeat(7, i32("Resistances"))), // +0x520
	when(FlagOwner, repeat(7,
		i32("BonusResistanceMods"), // +0x53C
		i32("ManaCostModifier"),    // +0x558
	)),
	i32("BaseMana"),                    // +0x1E4
	when(FlagOwner, i32("BaseHealth")), // +0x1E8
	u8("SheatheState"),                 // +0x1EC
	u8("PvpFlags"),                     // +0x1ED
	u8("PetFlags"),                     // +0x1EE
	u8("ShapeshiftForm"),               // +0x1EF
	// Owner-only attack-power block, +0x1F0..+0x248: 23 fields where the
	// fork writes 20 (it writes the other 3 as owner-only int32s after
	// TaxiNodesID, where the client reads none). Fields whose position the
	// two agree on carry the fork's name.
	when(FlagOwner,
		i32("AttackPower"),           // +0x1F0
		i32("AttackPowerModPos"),     // +0x1F4
		i32("AttackPowerModNeg"),     // +0x1F8
		f32("AttackPowerMultiplier"), // +0x1FC
		i32("AttackPowerModSupport"), // +0x200
		i32("RangedAttackPower"),     // +0x204
		f32("Unk_0x208"),
		f32("Unk_0x20C"),
		f32("Unk_0x210"),
		f32("Unk_0x214"),
		i32("Unk_0x218"),
		i32("Unk_0x21C"),
		i32("Unk_0x220"),
		f32("Unk_0x224"),
		i32("Unk_0x228"),
		i32("Unk_0x22C"),
		i32("Unk_0x230"),
		i32("Unk_0x234"),
		i32("Unk_0x238"),
		f32("Lifesteal"),          // +0x23C
		f32("MinRangedDamage"),    // +0x240
		f32("MaxRangedDamage"),    // +0x244
		f32("ManaCostMultiplier"), // +0x248
	),
	f32("MaxHealthModifier"),               // +0x24C
	f32("HoverHeight"),                     // +0x250
	i32("MinItemLevelCutoff"),              // +0x254
	i32("MinItemLevel"),                    // +0x258
	i32("MaxItemLevel"),                    // +0x25C
	i32("AzeriteItemLevel"),                // +0x260
	i32("WildBattlePetLevel"),              // +0x264
	i32("BattlePetCompanionExperience"),    // +0x268
	u32("BattlePetCompanionNameTimestamp"), // +0x26C
	i32("InteractSpellID"),                 // +0x270
	i32("ScaleDuration"),                   // +0x274
	i32("LooksLikeMountID"),                // +0x278
	i32("LooksLikeCreatureID"),             // +0x27C
	i32("LookAtControllerID"),              // +0x280
	i32("PerksVendorItemID"),               // +0x284
	i32("TaxiNodesID"),                     // +0x288
	guid("GuildGUID"),                      // +0x290
	u32("PassiveSpells.size"),              // +0x2A8
	u32("WorldEffects.size"),               // +0x2E0
	u32("ChannelObjects.size"),             // +0x318
	i32("FlightCapabilityID"),              // +0x348
	f32("GlideEventSpeedDivisor"),          // +0x34C
	i32("DriveCapabilityID"),               // +0x350
	i32("MaxHealthModifierFlatNeg"),        // +0x354
	i32("MaxHealthModifierFlatPos"),        // +0x358
	u32("SilencedSchoolMask"),              // +0x35C
	when(FlagOwner,
		f32("Unk_0x360"),
		u32("Unk_0x368.size"), // list read by 0x1008d4d24
		each("Unk_0x368.size", i32("Unk_0x368.A"), i32("Unk_0x368.B")),
	),
	u32("CurrentAreaID"), // +0x38C
	when(FlagOwner,
		guid("ClassicComboTarget"), // +0x390
		i32("Unk_0x3A0"),
		f32("Unk_0x3A4"),
	),
	f32("NameplateDistanceMod"),   // +0x3A8
	f32("AutoAttackRangeMod"),     // +0x3AC
	guid("NameplateAttachToGUID"), // +0x3B0
	each("PassiveSpells.size", i32("PassiveSpells.SpellID"), i32("PassiveSpells.AuraSpellID")),
	each("WorldEffects.size", i32("WorldEffects")),
	each("ChannelObjects.size", guid("ChannelObjects")),
	bit("Field_314"), // one byte at +0x388
	bit("HasAssistActionData"),
	opt("HasAssistActionData",
		u8("AssistActionData.Type"),
		u32("AssistActionData.VirtualRealmAddress"),
		bitsN("AssistActionData.PlayerName.len", 6),
		str("AssistActionData.PlayerName", "AssistActionData.PlayerName.len"),
	),
)

// superDistrict is the 4×u32 + i8 struct the client reads at PlayerData
// +0x5C and again at +0x458 (reader 0x100830af0 region); the fork says its
// first u32 is the character's super district (ruleset).
func superDistrict(prefix string) []op {
	return prefixed(prefix, ops(u32("SuperDistrictID"), u32("Unk1"), u32("Unk2"), u32("Unk3"), i8("Unk4")))
}

// questLog is QuestLog (client reader 0x1008e2dd4 region), 70 bytes.
var questLog = ops(
	i32("QuestID"),
	u16("StateFlags"),
	repeat(24, i16("ObjectiveProgress")),
	i64("EndTime"),
	u32("ObjectiveFlags"),
	u32("EnabledObjectivesMask"),
)

// itemInstance is WorldPackets::Item::ItemInstance (client 0x1008337d8).
var itemInstance = ops(
	i32("ItemID"),
	bitsN("Modifications.size", 7),
	flush(),
	each("Modifications.size", u8("Modifications.Type"), i32("Modifications.Value")),
	bit("HasItemBonus"),
	opt("HasItemBonus",
		u8("ItemBonus.Context"),
		u32("ItemBonus.BonusListIDs.size"),
		each("ItemBonus.BonusListIDs.size", i32("ItemBonus.BonusListIDs")),
	),
)

// PlayerData, reader 0x1008d2130.
var playerDataCreate = cat(ops(
	guid("DuelArbiter"),      // +0x0
	guid("WowAccount"),       // +0x10
	guid("BnetAccount"),      // +0x20
	u64("GuildClubMemberID"), // +0x30
	guid("LootTargetGUID"),   // +0x38
	u32("PlayerFlags"),       // +0x48
	u32("PlayerFlagsEx"),     // +0x4C
	u32("GuildRankID"),       // +0x50
	u32("GuildDeleteDate"),   // +0x54
	i32("GuildLevel"),        // +0x58
), superDistrict("SuperDistrict."), ops( // +0x5C
	u32("Customizations.size"),       // +0x78
	u32("RandomCustomizations.size"), // +0xB0
	repeat(2, u8("PartyType")),       // +0x46C
	u8("NativeSex"),                  // +0xE0
	u8("Inebriation"),                // +0xE1
	u8("PvpTitle"),                   // +0xE2
	u8("ArenaFaction"),               // +0xE3
	u32("DuelTeam"),                  // +0xE4
	i32("GuildTimeStamp"),            // +0xE8
	when(FlagPartyMember,
		repeat(175, prefixed("QuestLog.", questLog)...), // +0x470, 0x48 each
		u32("QuestLogQuestIdToIndex.size"),              // map at +0xF0, reader 0x1008d4d24
		each("QuestLogQuestIdToIndex.size", i32("QuestLogQuestIdToIndex.Key"), i32("QuestLogQuestIdToIndex.Value")),
		u32("QuestSessionQuestLog.size"), // +0x118
	),
	repeat(19, prefixed("VisibleItems.", visibleItem)...), // +0x35A8
	i32("PlayerTitle"),                       // +0x148
	i32("FakeInebriation"),                   // +0x14C
	u32("VirtualPlayerRealm"),                // +0x150
	u32("CurrentSpecID"),                     // +0x154
	i32("CurrentCombatTraitConfigSubTreeID"), // +0x158
	i32("TaxiMountAnimKitID"),                // +0x15C
	repeat(6, f32("AvgItemLevel")),           // +0x3808
	u8("CurrentBattlePetBreedQuality"),       // +0x160
	i32("HonorLevel"),                        // +0x164
	i64("LogoutTime"),                        // +0x168
	u32("ArenaCooldowns.size"),               // +0x178
	repeat(32, i32("ForcedReactions.FactionID"), i32("ForcedReactions.Reaction")), // +0x3820
	i32("OfferedAdvJournalQuestID"),                                               // +0x2DC
	i32("OfferedScriptQuestID"),                                                   // +0x2E0
	i32("CurrentBattlePetSpeciesID"),                                              // +0x2E4
	u32("PetNames.size"),                                                          // +0x2F0
	u32("CtrOptions.ConditionalFlags.size"),                                       // +0x328
	u8("CtrOptions.FactionGroup"),                                                 // +0x338
	u32("CtrOptions.ChromieTimeExpansionMask"),
	each("CtrOptions.ConditionalFlags.size", u32("CtrOptions.ConditionalFlags")),
	i32("CovenantID"),                             // +0x344
	i32("SoulbindID"),                             // +0x348
	f32("DungeonScore.OverallScoreCurrentSeason"), // +0x350
	f32("DungeonScore.LadderScoreCurrentSeason"),  // +0x354
	u32("DungeonScore.Runs.size"),                 // +0x360
	each("DungeonScore.Runs.size",
		i32("DungeonScore.Runs.ChallengeModeID"),
		f32("DungeonScore.Runs.MapScore"),
		i32("DungeonScore.Runs.BestRunLevel"),
		i32("DungeonScore.Runs.BestRunDurationMS"),
		u8("DungeonScore.Runs.Unknown1110"),
		bit("DungeonScore.Runs.FinishedSuccess"),
	),
	guid("LeaverInfo.BnetAccountGUID"),
	f32("LeaverInfo.LeaveScore"),           // +0x384
	u32("LeaverInfo.SeasonID"),             // +0x388
	u32("LeaverInfo.TotalLeaves"),          // +0x38C
	u32("LeaverInfo.TotalSuccesses"),       // +0x390
	i32("LeaverInfo.ConsecutiveSuccesses"), // +0x394
	i64("LeaverInfo.LastPenaltyTime"),      // +0x398
	i64("LeaverInfo.LeaverExpirationTime"), // +0x3A0
	i32("LeaverInfo.Flags"),                // +0x3A8
	bit("LeaverInfo.IsLeaver"),             // byte at +0x380
	guid("SpectateTarget"),
	i32("WorldLootObjectInventorySwapSlot"),                          // +0x3C0
	repeat(16, prefixed("VisibleEquipableSpells.", itemInstance)...), // +0x3920, 0x70 each
	u32("VisualItemReplacements.size"),                               // +0x3D0
	repeat(19, u32("PlunderstormItemDisplayID")),                     // +0x4020
	i32("PersonalTabard.EmblemStyle"),                                // +0x410
	i32("PersonalTabard.EmblemColor"),
	i32("PersonalTabard.BorderStyle"),
	i32("PersonalTabard.BorderColor"),
	i32("PersonalTabard.BackgroundColor"),
	i32("NpcAsPlayerInfo.Field_0"), // +0x428
	i32("NpcAsPlayerInfo.CharacterLoadoutID"),
	i32("NpcAsPlayerInfo.CreatureID"),
	f32("NpcAsPlayerInfo.LocWorldSpace.X"), // +0x434
	f32("NpcAsPlayerInfo.LocWorldSpace.Y"),
	f32("NpcAsPlayerInfo.LocWorldSpace.Z"),
	f32("NpcAsPlayerInfo.FacingWorldSpace"),
	guid("NpcAsPlayerInfo.TransportGUID"),
), superDistrict("SuperDistrict2."), ops( // +0x458
	each("Customizations.size", u32("Customizations.ChrCustomizationOptionID"), u32("Customizations.ChrCustomizationChoiceID")),
	each("RandomCustomizations.size", u32("RandomCustomizations.ChrCustomizationOptionID"), u32("RandomCustomizations.ChrCustomizationChoiceID")),
	when(FlagPartyMember, each("QuestSessionQuestLog.size", prefixed("QuestSessionQuestLog.", questLog)...)),
	each("ArenaCooldowns.size",
		i32("ArenaCooldowns.SpellID"),
		i32("ArenaCooldowns.Charges"),
		u32("ArenaCooldowns.Flags"),
		u32("ArenaCooldowns.StartTime"),
		u32("ArenaCooldowns.EndTime"),
		u32("ArenaCooldowns.NextChargeTime"),
		u8("ArenaCooldowns.MaxCharges"),
	),
	each("PetNames.size",
		u32("PetNames.CreatureID"),
		bitsN("PetNames.Name.len", 8),
		str("PetNames.Name", "PetNames.Name.len"),
	),
	each("VisualItemReplacements.size", i32("VisualItemReplacements")),
	bitsN("Name.len", 6),
	bitsN("Surname.len", 9),
	when(FlagPartyMember, bit("HasQuestSession")),
	bit("HasLevelLink"),
	// Two optional five-string sets follow, at +0x400 and +0x408 (readers
	// 0x1008d0dec and 0x1008d15ec, identical). Each length is 7 bits; the
	// fork writes DeclinedNames with 10-bit lengths and has no second set.
	bit("HasDeclinedNames"),
	bit("HasUnk_0x408"),
	str("Name", "Name.len"),       // +0x1A8
	str("Surname", "Surname.len"), // +0x1D9
), declinedNames("HasDeclinedNames", "DeclinedNames."), declinedNames("HasUnk_0x408", "Unk_0x408."))

func declinedNames(has, prefix string) []op {
	return ops(opt(has, prefixed(prefix, ops(
		repeat(5, bitsN("Name.len", 7)),
		repeat(5, str("Name", "Name.len")),
	))...))
}

// GameObjectData, reader 0x1008b5fe8 (chain 0x1017ffcbc: ObjectData, then
// this). Real 70755 creates are consumed exactly by the 70707 reader
// (docs/EVIDENCE.md E31). The fork's Level is written twice: once at +0x74
// (its comment: the creator's level, 0 for world objects) and once at +0xBC.
var gameObjectDataCreate = ops(
	i32("DisplayID"),                         // +0x0
	u32("SpellVisualID"),                     // +0x4
	u32("StateSpellVisualID"),                // +0x8
	u32("SpawnTrackingStateAnimID"),          // +0xC
	u32("SpawnTrackingStateAnimKitID"),       // +0x10
	u32("StateWorldEffectIDs.size"),          // vector at +0x20
	u32("StateWorldEffectsQuestObjectiveID"), // +0x30
	each("StateWorldEffectIDs.size", u32("StateWorldEffectIDs")),
	guid("CreatedBy"),            // +0x38
	guid("GuildGUID"),            // +0x48
	u32("Flags"),                 // +0x58
	u32("FlagsB"),                // +0x5C
	f32("ParentRotation.X"),      // +0x60
	f32("ParentRotation.Y"),      // +0x64
	f32("ParentRotation.Z"),      // +0x68
	f32("ParentRotation.W"),      // +0x6C
	i32("FactionTemplate"),       // +0x70
	i32("CreatorLevel"),          // +0x74
	i8("State"),                  // +0x78
	i8("TypeID"),                 // +0x79
	u8("PercentHealth"),          // +0x7A
	u32("ArtKit"),                // +0x7C
	u32("EnableDoodadSets.size"), // vector at +0x88
	u32("CustomParam"),           // +0xB8
	i32("Level"),                 // +0xBC
	u32("AnimGroupInstance"),     // +0xC0
	u32("UiWidgetItemID"),        // +0xC4
	u32("UiWidgetItemQuality"),   // +0xC8
	u32("UiWidgetItemCount"),     // +0xCC
	u32("WorldEffects.size"),     // vector at +0xD8
	each("EnableDoodadSets.size", i32("EnableDoodadSets")),
	each("WorldEffects.size", i32("WorldEffects")),
	bit("HasAssistActionData"), // byte at +0x180
	// AssistActionData (reader 0x100851f08) at +0x108: a 6-bit and an
	// 11-bit length share the first bit group (0x100709878 continues the
	// byte), then the values, the player name, and the monster name as a
	// sized C string (0x1006c83a0: a length of 0 or 1 reads nothing).
	opt("HasAssistActionData", gameObjectAssistActionData...),
)

// gameObjectAssistActionData is read by 0x100851f08 in the create and the
// delta alike (delta: GameObjectData bit 27).
var gameObjectAssistActionData = ops(
	flush(), // the struct's reader starts its own bit group
	bitsN("AssistActionData.PlayerName.len", 6),
	bitsN("AssistActionData.MonsterName.len", 11),
	u32("AssistActionData.VirtualRealmAddress"), // +0x34
	u8("AssistActionData.Sex"),                  // +0x38
	i64("AssistActionData.Time"),                // +0x40
	i32("AssistActionData.DelveTier"),           // +0x48
	str("AssistActionData.PlayerName", "AssistActionData.PlayerName.len"),
	cstr("AssistActionData.MonsterName", "AssistActionData.MonsterName.len"),
)

// itemEnchantment is ItemEnchantment, 14 per item in the client (the fork
// has 13 and pads a fourteenth).
var itemEnchantment = ops(i32("ID"), u32("Duration"), i16("Charges"), u16("Inactive"))

// itemBonusKey is ItemBonusKey (reader 0x100834584).
var itemBonusKey = ops(
	i32("ItemID"),             // +0x130
	u32("BonusListIDs.size"),  // vector at +0x140
	u32("Modifications.size"), // vector at +0x158
	each("BonusListIDs.size", i32("BonusListIDs")),
	each("Modifications.size", u8("Modifications.Type"), i32("Modifications.Value")),
)

// ItemData, reader 0x1008c8c98 (chain 0x10183d678: ObjectData, then this).
// Real 70755 creates are consumed exactly by the 70707 reader (E31).
var itemDataCreate = cat(ops(
	guid("Owner"),       // +0x0
	guid("ContainedIn"), // +0x10
	guid("Creator"),     // +0x20
	guid("GiftCreator"), // +0x30
	when(FlagOwner,
		u32("StackCount"),              // +0x40
		u32("Expiration"),              // +0x44
		repeat(5, i32("SpellCharges")), // +0x16C
	),
	u32("DynamicFlags"), // +0x48
	repeat(14, prefixed("Enchantment.", itemEnchantment)...), // +0x180, 0xC each
	when(FlagOwner,
		u32("Durability"),    // +0x4C
		u32("MaxDurability"), // +0x50
	),
	u32("CreatePlayedTime"), // +0x54
	u8("Context"),           // +0x58
	i64("CreateTime"),       // +0x60
	when(FlagOwner,
		u64("ArtifactXP"),         // +0x68
		u8("ItemAppearanceModID"), // +0x70
	),
	// Modifiers: ItemModList (0x100833ef0 region), a 7-bit count in its
	// own byte, then the values.
	bitsN("Modifiers.size", 7),
	flush(),
	each("Modifiers.size", u8("Modifiers.Type"), i32("Modifiers.Value")),
	u32("ArtifactPowers.size"),        // vector at +0xC0
	u32("Gems.size"),                  // vector at +0xF8
	when(FlagOwner, u32("ZoneFlags")), // +0x128
), prefixed("ItemBonusKey.", itemBonusKey), ops(
	when(FlagOwner, u16("DEBUGItemLevel")), // +0x168
	each("ArtifactPowers.size",
		i16("ArtifactPowers.ArtifactPowerID"),
		u8("ArtifactPowers.PurchasedRank"),
		u8("ArtifactPowers.CurrentRankWithBonus"),
	),
	each("Gems.size",
		i32("Gems.ItemID"),
		repeat(16, u16("Gems.BonusListIDs")),
		u8("Gems.Context"),
	),
))

// vendorDataCreate is the FVendor_C fragment (wire id 18): one int32. The
// registry entry (0x102cf7788) gives id 0x12, kind 0 (not indirect) and a
// 4-byte data block; the fork writes VendorData::Flags.
var vendorDataCreate = ops(i32("Flags"))
