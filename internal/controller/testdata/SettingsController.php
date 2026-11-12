<?php
/*
 * Copyright (C) 2019 Michael Muenz <m.muenz@gmail.com>
 * Copyright (C) 2020 Deciso B.V.
 * Copyright (C) 2026 opnsense-api-spec contributors (shortened and modified for tests)
 * All rights reserved.
 *
 * Derived from opnsense/core src/opnsense/mvc/app/controllers/OPNsense/Unbound/Api/SettingsController.php.
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
/*
 * Fixture modelled on OPNsense core (BSD-2-Clause), shortened.
 * // not a comment end: */

namespace OPNsense\Demo\Api;

use OPNsense\Base\ApiMutableModelControllerBase;

class SettingsController extends ApiMutableModelControllerBase
{
    protected static $internalModelName = 'demo';
    protected static $internalModelClass = 'OPNsense\\Demo\\Demo';

    # public function hiddenAction() {}
    public function searchHostAction()
    {
        return $this->searchBase('hosts.host', null, 'hostname');
    }

    public function getHostAction($uuid = null)
    {
        return $this->getBase('host', 'hosts.host', $uuid);
    }

    public function addHostAction()
    {
        return $this->addBase("host", "hosts.host");
    }

    public function setHostAction($uuid)
    {
        return $this->setBase('host', 'hosts.host', $uuid);
    }

    public function delHostAction($uuid)
    {
        $this->delBase('aliases.alias', $uuid);
        return $this->delBase('hosts.host', $uuid);
    }

    public function getAction()
    {
        $data = parent::getAction();
        $data['demo']['x'] = '{';  // brace in a string
        return $data;
    }

    public function exportAction()
    {
        if ($this->request->isGet()) {
            return ['data' => $this->dynamicBase(self::$path)];
        }
        return [];
    }

    public function getPacAction($uuid = null)
    {
        return array("pac" => $this->getBase('match', 'pac.match', $uuid));
    }

    public function dynAction()
    {
        return $this->searchBase($this->path);
    }
}
