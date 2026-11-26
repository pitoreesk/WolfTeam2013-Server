"""Build the 2013 TR ServerNotice text set served at /softnyx/game/Notice/.

The client (CShell sub_341C5C00) downloads TextVersion.txt from the host in
Img\\ServerNotice\\NoticeInfo.txt and fetches every listed file whose version
is higher than the local one (docs/protocol/servernotice_2013.md).

Base: the newest Turkish text files available (Img\\ServerNotice of the
client, Sep 2012). Items sold by the 2013 shopdata but absent from them get a
name derived from WT_Attributes\\forge.dat (the client's own item list),
translated with the naming patterns of the Turkish file, and the description
of the most similar existing item of the same shop category.
"""
import csv
import difflib
import os
import re
import shutil
import struct

CLIENT = r"D:\WolfTeam2013"
SERVER = r"D:\WolfTeam2013\WolfTeam2013-Server"
# Pristine Sep 2012 TR copies; the 2013 client folder now holds our downloads.
NOTICE_IN = r"D:\WolfTeam2012\Img\servernotice"
OUT = os.path.join(SERVER, "notice")
VERSION = 10  # bump to make clients download again

# Items whose name is not in forge.dat (derived from their exitemdata icon).
MANUAL = {
    10033: "Özel Malzeme",
    14251: "Güneş Dövmesi Seti (GL)",
    14261: "Günlük Silah Seti",
    14262: "Günlük Malzeme Seti",
    14263: "Günlük Paket",
    14264: "Lüks Paket",
    14265: "Prestij Paketi",
    14266: "Majestik Paket",
    14331: "Türk Bayrağı Dövmesi Seti",
    17018: "Sevgililer Günü Jackpot Kutusu",
    19610: "Kurt Yeteneği - Hızlı Atılma",
    # Unlisted items without a forge.dat name (names from their icon file).
    33096: "Kurt Yeteneği - Hızlı Atılma (Özel)",
    14899: "Ramazan Paketi",
    30039: "Angela SF",
    30040: "Christopher SF",
    30041: "Adriana SF",
    30042: "Pedro SF",
    14238: "M-1014 MC Paketi",
    14239: "Haitai GL Dövmesi Paketi",
    14240: "Smilodon DR Dövmesi Paketi",
    14241: "I-115 T Flame Paketi",
    14260: "Yapay Zeka Malzemesi (Zor)",
    17017: "Yeni Yıl Jackpot Kutusu",
    17019: "Kurt Parası Kutusu",
    14322: "Yıldırım Dövmesi Seti",
    14323: "Yıldırım Dövmesi Seti (DR)",
    14324: "Yıldırım Dövmesi Seti (SP)",
    14325: "Yıldız Dövmesi Seti",
    14326: "Yıldız Dövmesi Seti (DB)",
    14327: "Yıldız Dövmesi Seti (SP)",
    14328: "Kasırga Dövmesi Seti",
    14329: "Kasırga Dövmesi Seti (DP)",
    14330: "Kasırga Dövmesi Seti (SP)",
}
SLOTS = {"B": "Sırt", "L": "Sol", "R": "Sağ"}
PARTS = [("Elbow Protector", "Dirseklik"), ("Knee Protector", "Dizlik"), ("Gloves", "Eldiven"),
         ("Boots", "Bot"), ("Goggles", "Gözlük"), ("Vest", "Yelek")]


def turkish(name):
    m = re.fullmatch(r"Tattoo ([BLR]) \((.+)\)", name)
    if m:
        return f"{m.group(2)} Dövmesi ({SLOTS[m.group(1)]})"
    m = re.fullmatch(r"Tattoo \((.+)\) Set", name)
    if m:
        return f"{m.group(1)} Dövmesi Seti"
    for en, tr in PARTS:
        if name.endswith(" " + en):
            return name[: -len(en)] + tr
    m = re.fullmatch(r"(.+) ([AB]) Parts Set", name)
    if m:
        return f"{m.group(1)} {m.group(2)} Parça Seti"
    name = name.replace("Wolf Look Package", "Kurt Görünüm Paketi")
    name = name.replace("Gold Look Package", "Altın Görünüm Paketi")
    return name


def read_text(path, strings):
    data = open(path, "rb").read()
    pos, out = 0, {}
    while pos < len(data):
        item_id, = struct.unpack_from("<H", data, pos)
        pos += 2
        values = []
        for _ in range(strings):
            n, = struct.unpack_from("<H", data, pos)
            pos += 2
            values.append(data[pos:pos + 2 * n].decode("utf-16le"))
            pos += 2 * n
        out[item_id] = values
    assert pos == len(data), path
    return out


def read_order(path, strings):
    """Record ids in file order, duplicates included (the client files are
    neither sorted nor unique; they are rewritten in the same order)."""
    data = open(path, "rb").read()
    pos, order = 0, []
    while pos < len(data):
        item_id, = struct.unpack_from("<H", data, pos)
        pos += 2
        for _ in range(strings):
            n, = struct.unpack_from("<H", data, pos)
            pos += 2 + 2 * n
        order.append(item_id)
    return order


def write_text(path, records, order=None):
    order = list(order or [])
    known = set(order)
    order += [i for i in sorted(records) if i not in known]
    with open(path, "wb") as fh:
        for item_id in order:
            fh.write(struct.pack("<H", item_id))
            for value in records[item_id]:
                raw = value.encode("utf-16le")
                fh.write(struct.pack("<H", len(raw) // 2))
                fh.write(raw)


def replace_string(data, key, text):
    """Rewrite every u16 id / u16 length / UTF-16 record with this id."""
    out, pos = bytearray(), 0
    while pos < len(data):
        item_id, n = struct.unpack_from("<HH", data, pos)
        raw = data[pos + 4:pos + 4 + 2 * n]
        if item_id == key:
            raw = text.encode("utf-16le")
        out += struct.pack("<HH", item_id, len(raw) // 2) + raw
        pos += 4 + 2 * n
    return bytes(out)


def main():
    os.makedirs(OUT, exist_ok=True)
    attributes = os.path.join(CLIENT, r"XFS\data\Root\wt_attributes")
    forge = {int(r["item_id"]): r["name"] for r in
             csv.DictReader(open(os.path.join(SERVER, r"docs\data\forge_2013.csv"), encoding="utf-8"))}
    shop = {}
    raw = open(os.path.join(attributes, "shopdata.dat"), "rb").read()
    for k in range(len(raw) // 350):
        rec = raw[k * 350:(k + 1) * 350]
        item_id, category = struct.unpack_from("<I", rec, 4)[0], struct.unpack_from("<I", rec, 17)[0]
        shop.setdefault(item_id, (rec[9], category))

    exitem = read_text(os.path.join(NOTICE_IN, "exitemtext.dat"), 3)
    weapon = read_text(os.path.join(NOTICE_IN, "weapontext.dat"), 5)
    added = []
    # Every shopdata item without text: the client also shows unlisted ones
    # (e.g. the character "Parts Set" packages 14284..14319 in the NEW tab).
    for item_id, (listed, category) in sorted(shop.items()):
        table, strings = (weapon, 5) if item_id < 10000 else (exitem, 3)
        if item_id in table:
            continue
        name = MANUAL.get(item_id) or (turkish(forge[item_id]) if item_id in forge else None)
        if name is None:
            continue
        # Description of the most similar named item in the same category.
        same = [i for i in table if shop.get(i, (0, None))[1] == category] or list(table)
        best = max(same, key=lambda i: difflib.SequenceMatcher(None, forge.get(i, table[i][0]), forge.get(item_id, name)).ratio())
        table[item_id] = [name] + table[best][1:strings]
        added.append((item_id, name, best))

    # Korean leftovers of the 2012 Turkish files (the official TR text for these
    # entries was never localised): W-Pack names/descriptions and broken words.
    for pack in range(14901, 15001):
        if pack in exitem:
            exitem[pack] = [f"K-Paket {pack - 14900}",
                            "Wolfteam'in sizin için hazırladığı hediye kutusu.\n\n"
                            "İçinde birbirinden farklı ürünler var,\nhemen göz atın!",
                            exitem[pack][2]]
    exitem[14017][1] = exitem[14017][1].replace("yalnızca 따\nMalzeme 'de", "yalnızca Özel\nMalzeme'den")
    upgrade = read_text(os.path.join(NOTICE_IN, "upgradetext.dat"), 4)
    for key, values in upgrade.items():
        upgrade[key] = [v.replace("ger다\n", "gerek\n").replace("yüks다\n", "yüksek\n") for v in values]
    for table in (exitem, weapon, upgrade):
        for key, values in table.items():
            assert not any("가" <= ch <= "힣" for v in values for ch in v), key
    write_text(os.path.join(OUT, "UpgradeText.dat"), upgrade, read_order(os.path.join(NOTICE_IN, "upgradetext.dat"), 4))
    write_text(os.path.join(OUT, "ExitemText.dat"), exitem, read_order(os.path.join(NOTICE_IN, "exitemtext.dat"), 3))
    write_text(os.path.join(OUT, "WeaponText.dat"), weapon, read_order(os.path.join(NOTICE_IN, "weapontext.dat"), 5))
    # StringTable: u16 id, u16 length, UTF-16 text. The 2013 Premium Shop page
    # (sub_342C4C50 / sub_342C4C00) uses ids 1343..1348, absent from every
    # table the client ships; the texts are ours.
    # The original table has duplicate ids, so it is kept byte for byte and
    # the missing records are appended.
    original = open(os.path.join(NOTICE_IN, "stringtable.dat"), "rb").read()
    # 1212 formats the Power User name prefix in chat (sub_3407AC90); the TR
    # "%s" glued it to the callsign ("PREFIXname").
    original = replace_string(original, 1212, "[%s] ")
    present = read_text(os.path.join(NOTICE_IN, "stringtable.dat"), 1)
    strings = {}
    for key, text in {
        # StaticText does not wrap (premiumshoppage.txt FontRect x,y,w,h):
        # short lines. 1343 shares its first line with the right-aligned
        # target number (PremiumShopReachGoalCashText).
        1343: "Bu ay hedef tutar:\n\nkadar Nakit harcayan oyunculara\nözel ürünler satılır.",
        1344: "Harcamalar her ayın başında\nsıfırlanır.",
        1345: "Bu Ayki Harcamam",
        1346: "Hedef tutara henüz\nulaşmadınız.",
        1347: "Hedefe ulaştınız! Ürünleri\nsatın alabilirsiniz.",
        1348: "Hedef Tutar",
        # CS_FD_REWARDPLAYTIME_ACK text (sub_34177A30, %d = P.T. bonus Gold).
        1366: "Oyun süresi ödülü: %d Altın",
    }.items():
        if key not in present:
            strings[key] = [text]
    # In-game ranking menu (RankingSection, docs/protocol/ranking_2013.md):
    # combo, row, memo and "my ranking" texts 9501..9685 are missing from the
    # 2013 TR table. Texts from the later TR client table.
    RANKING_STRINGS = {
        9501: 'Sıralama standart oyun ile yükselir.\nBir hafta oynamayan oyuncu, haftalık sıralamaya\ngiremez.\nBir aydır oynamayan ise, aylık sıralamaya \ngiremez.',
        9502: 'Kişisel sıralama, kişisel sıralama ve oyun bilgisine\ngöre belirlenir.',
        9503: 'Klan sıralaması, Klan sıralaması ve oyun bilgisine\ngöre belirlenir.',
        9504: '1. 2. veya 3. olan oyuncu yorum yazabilir.',
        9505: '1. 2. veya 3. olan klanın kurucusu yorum yazabilir.',
        9506: '( ',
        9507: '%s)',
        9508: '%s (%d%%)',
        9509: 'Buraya yazabilirsin.',
        9510: '%s yer',
        9511: '%s yer)',
        9512: '%s',
        9515: 'Kayıt : %s \nKazanma : %s \nKaybetme : %s \nKazanma oranı : %d%%',
        9516: '%s Öldürme',
        9517: '%s GP',
        9518: '%s Puan',
        9519: '%s kaydı. ',
        9520: '%s ölürme kaydı ',
        9521: '%s GP si ',
        9522: '%s Klan puanı kaydı',
        9523: '%s Klan kaydı',
        9524: '-',
        9525: 'Günlük',
        9526: 'Haftalık',
        9527: 'Aylık',
        9528: 'Toplam',
        9530: 'Öldürme',
        9531: 'Kayıt',
        9532: 'GP',
        9533: 'Ölüm',
        9534: 'SP',
        9535: 'Klan Öld',
        9536: 'Klan Rekoru',
        9537: 'Klan Puanı',
        9538: 'Klan Ölm',
        9540: 'Hepsi',
        9541: 'Tüfek',
        9542: 'Pompalı',
        9543: 'Keskin nişancı',
        9544: 'Ağır Silah',
        9545: 'Tabanca',
        9546: 'Bıçak',
        9547: 'El Bombası',
        9548: 'Arbalet',
        9549: 'Temel Kurt',
        9550: 'Güçlü Kurt',
        9551: 'Hayalet Kurt',
        9552: 'Gardian Kurt',
        9553: 'Buz Kurt',
        9554: 'Hayalet Kurt 2',
        9555: 'Zeki Kurt',
        9556: 'Çılgın Kurt',
        9557: 'Deli Kurt',
        9558: 'Kurt Öldürme',
        9559: 'Kurt Kralı öldürme',
        9560: 'Zombi Kurt Öldürme',
        9561: 'Bebek Kurt',
        9562: 'Savaş Kurdu',
        9563: 'Özel Kurt',
        9564: 'Kan Kurdu',
        9565: 'Canavar Kurt',
        9566: 'Ölüm Kurdu',
        9567: 'Usta Kurt',
        9568: 'Mükemmel Kurt',
        9569: 'Bıçak - Karşı saldırı',
        9570: 'Tam Kafadan',
        9571: 'Rakip Öldürme',
        9572: 'Sinsi Atak',
        9573: 'Düşerek ölme',
        9574: 'Kendini öldürme',
        9650: 'HEPSİ',
        9651: 'İnsan',
        9652: 'Kurt',
        9653: 'Bot',
        9660: '%s öldürme kaydı',
        9661: '%s Ölüm kaydı',
        9662: '%s kaydı. ',
        9663: '%s GP kaydı',
        9670: '%s Klan öldürme kaydı',
        9671: '%s Klan ölüm kaydı.',
        9672: '%s Klan kaydı',
        9673: '%s Klan puanı',
        9680: 'sıralama dışında',
        9681: '%d ay %d gün',
        9682: '%d ay %d hafta',
        9683: '%d ay',
        9685: 'Rütbe sicili - %s sıralaması',
    }
    for key, text in RANKING_STRINGS.items():
        if key not in present:
            strings[key] = [text]
    # USEITEM reply texts (sub_34226820: title 7000 + use type, body 7500 +
    # type) missing for the newer coupons; amounts from their icon names.
    import sys
    sys.path.insert(0, os.path.join(SERVER, "tools", "exitem"))
    import gen_exitem_use
    for item, kind, icon in gen_exitem_use.records(open(gen_exitem_use.CLIENT, "rb").read()):
        m = re.match(r"(\d+)_?(GOLD|WC|GP)\.", icon.split("\\")[-1], re.I)
        if not m or 7000 + kind in present or 7500 + kind in present:
            continue
        amount = f"{int(m.group(1)):,}".replace(",", ".")
        unit = {"GOLD": "Altın", "WC": "KP", "GP": "GP"}[m.group(2).upper()]
        strings[7000 + kind] = [f"{amount} {unit} Kuponunu kullan!"]
        strings[7500 + kind] = [f"{amount} {unit} hesabına eklendi!"]
    write_text(os.path.join(OUT, "StringTable.add"), strings)
    with open(os.path.join(OUT, "StringTable.dat"), "wb") as fh:
        fh.write(original + open(os.path.join(OUT, "StringTable.add"), "rb").read())
    os.remove(os.path.join(OUT, "StringTable.add"))
    # Client parser sub_341C4130: "<file>\t<version>" per line (sscanf "%s%d").
    with open(os.path.join(OUT, "TextVersion.txt"), "w", newline="\r\n") as fh:
        for name in ("ExitemText.dat", "UpgradeText.dat", "WeaponText.dat", "StringTable.dat"):
            fh.write(f"{name}\t{VERSION}\n")
    # Notice images: keep the client's own copies (version 0 = no download).
    for name, prefix in (("NoticeVersion.txt", "sl_notice"), ("EventNoticeVersion.txt", "sl_eventnotice")):
        with open(os.path.join(OUT, name), "w", newline="\r\n") as fh:
            for n in range(1, 6):
                fh.write(f"{prefix}{n}.png\t0\n" if prefix == "sl_notice" else f"{prefix}{n}.dtx\t0\n")
    with open(os.path.join(SERVER, r"docs\data\servernotice_added_2013.csv"), "w", encoding="utf-8", newline="") as fh:
        w = csv.writer(fh)
        w.writerow(["item_id", "name", "description_from"])
        w.writerows(added)
    print("added", len(added), "names; exitem", len(exitem), "weapon", len(weapon))


if __name__ == "__main__":
    main()
