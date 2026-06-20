package codemode

import "github.com/dop251/goja"

// goja aliases Number.prototype.toLocaleString to Number.prototype.toString,
// so any locale argument is parsed as a radix: (1234.5).toLocaleString("en-GB")
// throws `toString() radix argument must be between 2 and 36`, and there is no
// Intl in the engine at all. That crashes otherwise-correct agent scripts the
// moment they format money or large numbers.
//
// This installs a dependency-free Number.prototype.toLocaleString that never
// throws on a locale argument and produces sensible grouped output. It is NOT a
// full Intl implementation — the engine ships no locale data — so grouping is
// comma-thousands / dot-decimal (en-US/en-GB style) regardless of the locale
// tag, which covers the overwhelmingly common case. Symbol-less currencies and
// currencyDisplay:"code" render the ISO code, a space, then the amount.
// Date.prototype.toLocale* and String.prototype.localeCompare already work in
// goja and are left alone.
const numberLocalePolyfillSource = `
(function () {
  'use strict';
  var SP = String.fromCharCode(32);
  function group(intStr) {
    var out = '';
    var n = intStr.length;
    for (var i = 0; i < n; i++) {
      if (i > 0 && (n - i) % 3 === 0) out += ',';
      out += intStr.charAt(i);
    }
    return out;
  }
  // Single-character symbols only; any other currency falls back to its ISO
  // code plus a space ("CHF 1,234.50"), matching Intl's shape for code display.
  var SYMBOLS = {
    USD: '$', CAD: '$', AUD: '$', NZD: '$', HKD: '$', SGD: '$', MXN: '$',
    GBP: '£', EUR: '€', JPY: '¥', CNY: '¥',
    INR: '₹', KRW: '₩', RUB: '₽', BRL: 'R$', ZAR: 'R'
  };
  // Currencies that conventionally carry no minor unit.
  var ZERO_DECIMAL = { JPY: true, KRW: true, CLP: true, ISK: true, HUF: true, VND: true };

  function format(value, locales, options) {
    options = options || {};
    var num = Number(value);
    if (num !== num) return 'NaN';
    if (num === Infinity) return '∞';
    if (num === -Infinity) return '-∞';

    var style = options.style || 'decimal';
    var minFrac, maxFrac, defFrac;
    if (style === 'currency') {
      defFrac = ZERO_DECIMAL[options.currency] ? 0 : 2;
      minFrac = options.minimumFractionDigits != null ? options.minimumFractionDigits : defFrac;
      maxFrac = options.maximumFractionDigits != null ? options.maximumFractionDigits : (defFrac > minFrac ? defFrac : minFrac);
    } else if (style === 'percent') {
      num = num * 100;
      minFrac = options.minimumFractionDigits != null ? options.minimumFractionDigits : 0;
      maxFrac = options.maximumFractionDigits != null ? options.maximumFractionDigits : minFrac;
    } else {
      minFrac = options.minimumFractionDigits != null ? options.minimumFractionDigits : 0;
      maxFrac = options.maximumFractionDigits != null ? options.maximumFractionDigits : (minFrac > 3 ? minFrac : 3);
    }
    minFrac = Math.max(0, Math.min(20, minFrac | 0));
    maxFrac = Math.max(minFrac, Math.min(20, maxFrac | 0));

    var neg = num < 0;
    var abs = Math.abs(num);
    var fixed = abs.toFixed(maxFrac);

    // Trim trailing zeros down to minimumFractionDigits.
    if (maxFrac > minFrac && fixed.indexOf('.') >= 0) {
      var dot = fixed.split('.');
      var dec = dot[1];
      var keep = dec.length;
      while (keep > minFrac && dec.charAt(keep - 1) === '0') keep--;
      dec = dec.substr(0, keep);
      fixed = keep > 0 ? dot[0] + '.' + dec : dot[0];
    }

    var parts = fixed.split('.');
    var intPart = parts[0];
    var decPart = parts.length > 1 ? parts[1] : '';
    var grouped = options.useGrouping === false ? intPart : group(intPart);
    var body = decPart ? grouped + '.' + decPart : grouped;
    var sign = neg ? '-' : '';

    if (style === 'currency') {
      var code = options.currency || 'USD';
      var sym = SYMBOLS[code];
      if (!sym || options.currencyDisplay === 'code') {
        return sign + code + SP + body;
      }
      return sign + sym + body;
    }
    if (style === 'percent') {
      return sign + body + '%';
    }
    return sign + body;
  }

  Object.defineProperty(Number.prototype, 'toLocaleString', {
    value: function (locales, options) { return format(this, locales, options); },
    writable: true,
    enumerable: false,
    configurable: true
  });
})();
`

// numberLocalePolyfillProgram is compiled once at package load and replayed into
// every fresh VM. A *goja.Program is stateless and safe to share across runtimes.
var numberLocalePolyfillProgram = goja.MustCompile(
	"number_locale_polyfill.js", numberLocalePolyfillSource, false)

// installNumberLocalePolyfill runs the Number.prototype.toLocaleString polyfill
// into vm. Called during sandbox setup before user code executes.
func installNumberLocalePolyfill(vm *goja.Runtime) error {
	_, err := vm.RunProgram(numberLocalePolyfillProgram)
	return err
}
