package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

const dir = "internal/i18n/locales"

// Clés manquantes en/fr/ar (confirmées par le diagnostic — union des clés)
var missingKeys = map[string]map[string]string{
	"en": {
		"BET_AMOUNT_REQUIRED": "Bet amount required",
		"CURRENT_MULTIPLIER":  "Current multiplier: {multiplier}",
		"NEXT_ROUND":          "Next round in {seconds} seconds",
		"PAYOUT_AMOUNT":       "Payout amount: {amount}",
	},
	"fr": {
		"BET_AMOUNT_REQUIRED": "Montant du pari requis",
		"CURRENT_MULTIPLIER":  "Multiplicateur actuel : {multiplier}",
		"NEXT_ROUND":          "Prochaine manche dans {seconds} secondes",
		"PAYOUT_AMOUNT":       "Montant du gain : {amount}",
	},
	"ar": {
		"BET_AMOUNT_REQUIRED": "مبلغ الرهان مطلوب",
		"CURRENT_MULTIPLIER":  "المضاعف الحالي: {multiplier}",
		"NEXT_ROUND":          "الجولة التالية خلال {seconds} ثانية",
		"PAYOUT_AMOUNT":       "مبلغ الدفع: {amount}",
	},
}

// Placeholders réinjectés dans les 17 locales legacy (traductions préservées,
// placeholder ajouté au point d'insertion naturel de la langue)
var placeholderFixes = map[string]map[string]string{
	"es": {
		"BET_PLACED": "Apuesta realizada: {amount}", "BET_CASHED_OUT": "Apuesta retirada a {multiplier}x",
		"CRASH": "Caída a {multiplier}x", "ROUND_START": "Ronda iniciada en {seconds} segundos",
		"MIN_BET": "Apuesta mínima: {amount}", "MAX_BET": "Apuesta máxima: {amount}",
	},
	"pt": {
		"BET_PLACED": "Aposta realizada: {amount}", "BET_CASHED_OUT": "Aposta retirada a {multiplier}x",
		"CRASH": "Queda a {multiplier}x", "ROUND_START": "Rodada iniciada em {seconds} segundos",
		"MIN_BET": "Aposta mínima: {amount}", "MAX_BET": "Aposta máxima: {amount}",
	},
	"de": {
		"BET_PLACED": "Wette platziert: {amount}", "BET_CASHED_OUT": "Wette ausgezahlt bei {multiplier}x",
		"CRASH": "Absturz bei {multiplier}x", "ROUND_START": "Runde startet in {seconds} Sekunden",
		"MIN_BET": "Mindesteinsatz: {amount}", "MAX_BET": "Maximaleinsatz: {amount}",
	},
	"it": {
		"BET_PLACED": "Scommessa piazzata: {amount}", "BET_CASHED_OUT": "Scommessa incassata a {multiplier}x",
		"CRASH": "Crash a {multiplier}x!", "ROUND_START": "Il round inizia tra {seconds} secondi",
		"MIN_BET": "Puntata minima: {amount}", "MAX_BET": "Puntata massima: {amount}",
	},
	"ru": {
		"BET_PLACED": "Ставка принята: {amount}", "BET_CASHED_OUT": "Ставка выплачена при {multiplier}x",
		"CRASH": "Краш на {multiplier}x!", "ROUND_START": "Раунд начинается через {seconds} сек.",
		"MIN_BET": "Минимальная ставка: {amount}", "MAX_BET": "Максимальная ставка: {amount}",
	},
	"tr": {
		"BET_PLACED": "Bahis yapıldı: {amount}", "BET_CASHED_OUT": "Bahis {multiplier}x'te çekildi",
		"CRASH": "{multiplier}x'te çöküş!", "ROUND_START": "Tur {seconds} saniye içinde başlıyor",
		"MIN_BET": "Minimum bahis: {amount}", "MAX_BET": "Maksimum bahis: {amount}",
	},
	"sw": {
		"BET_PLACED": "Dau limewekwa: {amount}", "BET_CASHED_OUT": "Dau limelipwa kwa {multiplier}x",
		"CRASH": "Mchezo umeanguka kwa {multiplier}x!", "ROUND_START": "Mzunguko unaanza baada ya sekunde {seconds}",
		"MIN_BET": "Dau la chini: {amount}", "MAX_BET": "Dau la juu: {amount}",
	},
	"ha": {
		"BET_PLACED": "An sanya fare: {amount}", "BET_CASHED_OUT": "An cire kudin fare a {multiplier}x",
		"CRASH": "Fashewa a {multiplier}x!", "ROUND_START": "Zagaye ya fara cikin daƙiƙa {seconds}",
		"MIN_BET": "Mafi ƙarancin fare: {amount}", "MAX_BET": "Mafi girman fare: {amount}",
	},
	"yo": {
		"BET_PLACED": "A ti fi tẹtẹ sílẹ̀: {amount}", "BET_CASHED_OUT": "A ti san tẹtẹ náà jáde ní {multiplier}x",
		"CRASH": "Ìparun ní {multiplier}x!", "ROUND_START": "Ìyípo bẹ̀rẹ̀ ní ìṣẹ́jú-aaya {seconds}",
		"MIN_BET": "Tẹtẹ tó kéré jù: {amount}", "MAX_BET": "Tẹtẹ tó pọ̀ jù: {amount}",
	},
	"zu": {
		"BET_PLACED": "Ukubheja kufakiwe: {amount}", "BET_CASHED_OUT": "Ukubheja kukhokhiwe ku-{multiplier}x",
		"CRASH": "I-Crash ku-{multiplier}x!", "ROUND_START": "Umjikelezo uyaqala emasekhondini angu-{seconds}",
		"MIN_BET": "Ukubheja okuncane: {amount}", "MAX_BET": "Ukubheja okuphezulu: {amount}",
	},
	"zh": {
		"BET_PLACED": "下注成功：{amount}", "BET_CASHED_OUT": "已在 {multiplier}x 兑现下注",
		"CRASH": "在 {multiplier}x 爆炸!", "ROUND_START": "回合将在 {seconds} 秒后开始",
		"MIN_BET": "最低下注：{amount}", "MAX_BET": "最高下注：{amount}",
	},
	"ja": {
		"BET_PLACED": "ベットしました：{amount}", "BET_CASHED_OUT": "{multiplier}倍でキャッシュアウトしました",
		"CRASH": "{multiplier}倍でクラッシュ！", "ROUND_START": "ラウンドは {seconds} 秒後に開始",
		"MIN_BET": "最低ベット額：{amount}", "MAX_BET": "最高ベット額：{amount}",
	},
	"ko": {
		"BET_PLACED": "베팅 완료: {amount}", "BET_CASHED_OUT": "{multiplier}배에서 캐시아웃 완료",
		"CRASH": "{multiplier}배에서 크래시!", "ROUND_START": "{seconds}초 후 라운드 시작",
		"MIN_BET": "최소 베팅: {amount}", "MAX_BET": "최대 베팅: {amount}",
	},
	"th": {
		"BET_PLACED": "วางเดิมพันแล้ว: {amount}", "BET_CASHED_OUT": "ถอนเงินเดิมพันแล้วที่ {multiplier}x",
		"CRASH": "แครชที่ {multiplier}x!", "ROUND_START": "รอบเริ่มในอีก {seconds} วินาที",
		"MIN_BET": "เดิมพันขั้นต่ำ: {amount}", "MAX_BET": "เดิมพันสูงสุด: {amount}",
	},
	"hi": {
		"BET_PLACED": "बेट लगा दी गई: {amount}", "BET_CASHED_OUT": "{multiplier}x पर बेट कैश आउट कर दी गई",
		"CRASH": "{multiplier}x पर क्रैश!", "ROUND_START": "राउंड {seconds} सेकंड में शुरू हो रहा है",
		"MIN_BET": "न्यूनतम बेट: {amount}", "MAX_BET": "अधिकतम बेट: {amount}",
	},
	"bn": {
		"BET_PLACED": "বাজি গ্রহণ করা হয়েছে: {amount}", "BET_CASHED_OUT": "{multiplier}x এ বাজির টাকা তোলা হয়েছে",
		"CRASH": "{multiplier}x এ ক্র্যাশ!", "ROUND_START": "রাউন্ড {seconds} সেকেন্ড পরে শুরু হচ্ছে",
		"MIN_BET": "সর্বনিম্ন বাজি: {amount}", "MAX_BET": "সর্বোচ্চ বাজি: {amount}",
	},
	"fa": {
		"BET_PLACED": "شرط ثبت شد: {amount}", "BET_CASHED_OUT": "شرط در {multiplier}x تسویه شد",
		"CRASH": "کرش در {multiplier}x!", "ROUND_START": "دور تا {seconds} ثانیه دیگر شروع می‌شود",
		"MIN_BET": "حداقل شرط: {amount}", "MAX_BET": "حداکثر شرط: {amount}",
	},
}

func main() {
	locales := []string{"en", "fr", "es", "pt", "de", "it", "ru", "tr", "sw", "ha",
		"yo", "zu", "ar", "zh", "ja", "ko", "th", "hi", "bn", "fa"}

	for _, code := range locales {
		path := filepath.Join(dir, code+".json")
		raw, err := os.ReadFile(path)
		if err != nil {
			log.Fatalf("read %s: %v", path, err)
		}

		var doc map[string]interface{}
		if err := json.Unmarshal(raw, &doc); err != nil {
			log.Fatalf("parse %s: %v", path, err)
		}

		keysRaw, ok := doc["keys"].(map[string]interface{})
		if !ok {
			log.Fatalf("%s: champ 'keys' absent ou invalide", path)
		}

		changed := false

		if add, ok := missingKeys[code]; ok {
			for k, v := range add {
				if _, exists := keysRaw[k]; !exists {
					keysRaw[k] = v
					changed = true
				}
			}
		}

		if fix, ok := placeholderFixes[code]; ok {
			for k, v := range fix {
				keysRaw[k] = v
				changed = true
			}
		}

		if !changed {
			fmt.Printf("SKIPPED (aucun changement): %s.json\n", code)
			continue
		}

		doc["keys"] = keysRaw

		out, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			log.Fatalf("marshal %s: %v", path, err)
		}
		out = append(out, '\n')

		if err := os.WriteFile(path, out, 0644); err != nil {
			log.Fatalf("write %s: %v", path, err)
		}

		fmt.Printf("PATCHED: %s.json\n", code)
	}
}
