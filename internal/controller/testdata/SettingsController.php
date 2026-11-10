<?php

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
