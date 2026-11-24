<?php
// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause
//
// Shortened from OPNsense\Auth\FieldTypes\ApiKeyField and StoreB64Field
// and OPNsense\Firewall\FieldTypes\AliasContentField (26.7.9).

namespace OPNsense\Demo\FieldTypes;

use OPNsense\Base\FieldTypes\BaseField;
use OPNsense\Base\FieldTypes\TextField;

class ApiKeyField extends BaseField
{
    protected $internalIsContainer = false;

    public function __toString()
    {
        return "";
    }

    public function setValue($value)
    {
        if (is_a($value, 'SimpleXMLElement') && isset($value->item)) {
            /* auto convert to simple text blob */
            $tmp = [];
            foreach ($value->children() as $child) {
                $tmp[] = sprintf("%s|%s", $child->key, $child->secret);
            }
            return parent::setValue(implode("\n", $tmp));
        } elseif (!empty($value)) {
            /* update only */
            return parent::setValue($value);
        }
    }
}

class B64Field extends TextField
{
    public function __toString()
    {
        return base64_decode($this->getValue()) ?? '';
    }

    public function setValue($value)
    {
        if (is_a($value, 'SimpleXMLElement')) {
            return parent::setValue($value);
        } elseif (!empty($value)) {
            return parent::setValue(base64_encode($value));
        }

        return parent::setValue('');
    }
}

class ContentField extends BaseField
{
    private $separatorchar = "\n";

    public function getNodeData()
    {
        $result = array ();
        $selectlist = explode($this->separatorchar, (string)$this);
        foreach ($selectlist as $optKey) {
            $result[$optKey] = array("value" => $optKey, "selected" => 1);
        }
        return $result;
    }
}

class AddressListField extends BaseField
{
    public function getNodeData()
    {
        $result = [];
        foreach (explode(',', $this->internalValue) as $net) {
            $result[$net] = array("value" => $net, "selected" => 1);
        }
        return $result;
    }
}
