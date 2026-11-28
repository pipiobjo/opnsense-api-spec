<?php
/*
 * Copyright (C) 2015-2026 Deciso B.V.
 * Copyright (C) 2026 opnsense-api-spec contributors (shortened and modified for tests)
 * All rights reserved.
 *
 * Derived from opnsense/core src/opnsense/mvc/app/models/OPNsense/Base/FieldTypes/*.php.
 *
 * Redistribution and use in source and binary forms, with or without
 * modification, are permitted provided that the following conditions are met:
 *
 * 1. Redistributions of source code must retain the above copyright notice,
 *    this list of conditions and the following disclaimer.
 *
 * 2. Redistributions in binary form must reproduce the above copyright
 *    notice, this list of conditions and the following disclaimer in the
 *    documentation and/or other materials provided with the distribution.
 *
 * THIS SOFTWARE IS PROVIDED ``AS IS'' AND ANY EXPRESS OR IMPLIED WARRANTIES,
 * INCLUDING, BUT NOT LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY
 * AND FITNESS FOR A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE
 * AUTHOR BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY,
 * OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF
 * SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS
 * INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN
 * CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE)
 * ARISING IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE
 * POSSIBILITY OF SUCH DAMAGE.
 *
 * SPDX-License-Identifier: BSD-2-Clause
 */
namespace OPNsense\Base\FieldTypes;

abstract class BaseField { protected $internalDefaultValue = ""; public function setValue($value) {} public function __toString() { return (string)$this->internalValue; } }  /* the framework itself: not a conversion */
abstract class BaseListField extends BaseField { public function setOptionValues($data) { $this->internalOptionList = $data; } }  /* the framework: options from the model */
class BaseSetField extends BaseField
{
    protected $internalFieldSeparator = ',';
    protected $internalAsList = false;
}
class CSVListField extends BaseSetField { protected $internalAsList = true; }
class BooleanField extends BaseField { protected $internalDefaultValue = "0"; }
class IntegerField extends BaseField { public function setMinimumValue($v) {} public function setMaximumValue($v) {} }
class ArrayField extends BaseField {}
class OptionField extends BaseListField {}
class ModelRelationField extends BaseListField {}
class PortField extends BaseListField {}
class InterfaceField extends BaseListField {}
class HostnameField extends BaseSetField {}
class NetworkField extends BaseSetField {}
class TextField extends BaseField {}
class UniqueIdField extends BaseField {}
class ContainerField extends BaseField {}
/* a core class with checks, for the tests of validation.go (registered there by the test) */
class DemoRangeField extends BaseField
{
    public function setMinimumValue($value) {}
    public function getValidators()
    {
        $validators = parent::getValidators();
        return $validators;
    }
}
class MacAddressField extends BaseField { public function setValue($value) { parent::setValue(strtolower($value)); } }
/* as in 26.7.9 */
class UpdateOnlyTextField extends TextField
{
    public function __toString()
    {
        return "";
    }

    public function setValue($value)
    {
        if ($value != "") {
            parent::setValue($value);
        }
    }
}
/* as in 26.7.9 */
class LegacyLinkField extends BaseField
{
    private $internalSourceLink = null;

    public function __toString()
    {
        if (!empty($this->internalSourceLink)) {
            return (string)$this->internalSourceLink;
        }
        return (string)null;
    }

    public function setValue($value)
    {
        return;
    }
}
